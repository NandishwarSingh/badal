package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
)

// Reciprocal Rank Fusion. k=60 is the value from the original paper and is
// deliberately large: it flattens the contribution of top ranks so a document
// that both retrievers merely like beats one that a single retriever loves.
// That is the whole point of fusing -- agreement should outrank enthusiasm.
const rrfK = 60.0

// orTSQuery builds an OR tsquery from free text.
//
// websearch_to_tsquery ANDs every term, so "free food on campus" requires all
// three words in one chunk and matches nothing. Lexical retrieval wants OR
// with ranking: return anything containing any term, and let ts_rank_cd sort
// by how many and how rare. Stopwords are dropped so a query full of "on" and
// "the" does not drag in the whole corpus.
func orTSQuery(q string) string {
	var terms []string
	seen := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(q), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if len(w) < 2 || stop[w] || seen[w] {
			continue
		}
		seen[w] = true
		terms = append(terms, w)
		if len(terms) >= 24 {
			break
		}
	}
	return strings.Join(terms, " | ")
}

var stop = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "from": true,
	"that": true, "this": true, "what": true, "when": true, "where": true,
	"who": true, "how": true, "why": true, "are": true, "was": true,
	"you": true, "your": true, "can": true, "did": true, "does": true,
	"has": true, "have": true, "had": true, "will": true, "would": true,
	"about": true, "into": true, "onto": true, "there": true, "their": true,
}

type scored struct {
	Hit
	vecRank, ftsRank int
	rrf              float64
}

// Hybrid runs vector and full-text retrieval over the same chunks and fuses
// the rankings.
//
// Neither half is sufficient alone. Dense retrieval misses exact tokens --
// course codes, room numbers, surnames -- because they carry little semantic
// weight. BM25 misses paraphrase entirely: "free food" never matches "mess".
func (s *Service) Hybrid(ctx context.Context, query string, limit int, types []string) ([]Hit, error) {
	if strings.TrimSpace(query) == "" {
		return []Hit{}, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	// Over-fetch from both arms: fusion and reranking both need candidates
	// deeper than the final page to have anything to reorder.
	depth := limit * 4
	if depth < 40 {
		depth = 40
	}

	vecs, err := s.Embed(ctx, []string{query})
	if err != nil {
		return nil, err
	}
	q := vecString(vecs[0])

	tx, err := s.beginSearch(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Both arms bound their candidate set on the bare table before collapsing
	// chunks to one row per source, so the vector arm can use HNSW and the
	// lexical arm never ranks more rows than it is going to keep.
	rows, err := tx.Query(ctx, `
		WITH knn AS (
		  SELECT source_type, source_id, content, parent, occurred_at,
		         embedding <=> $1::halfvec AS dist
		    FROM embeddings
		   WHERE ($4::text[] IS NULL OR source_type = ANY($4))
		   ORDER BY embedding <=> $1::halfvec
		   LIMIT $5
		), vec AS (
		  SELECT DISTINCT ON (source_type, source_id)
		         source_type, source_id, content, parent, occurred_at,
		         1 - dist AS sim
		    FROM knn
		   ORDER BY source_type, source_id, dist
		), vtop AS (
		  SELECT *, row_number() OVER (ORDER BY sim DESC) AS vrank
		    FROM vec ORDER BY sim DESC LIMIT $3
		), lex AS (
		  SELECT source_type, source_id, content, parent, occurred_at,
		         -- normalisation 32 divides by rank+1, so a long document does
		         -- not win purely by containing more words.
		         ts_rank_cd(content_tsv, to_tsquery('english', $2), 32) AS score
		    FROM embeddings
		   WHERE $2 <> '' AND content_tsv @@ to_tsquery('english', $2)
		     AND ($4::text[] IS NULL OR source_type = ANY($4))
		   ORDER BY score DESC
		   LIMIT $5
		), fts AS (
		  SELECT DISTINCT ON (source_type, source_id)
		         source_type, source_id, content, parent, occurred_at, score
		    FROM lex
		   ORDER BY source_type, source_id, score DESC
		), ftop AS (
		  SELECT *, row_number() OVER (ORDER BY score DESC) AS frank
		    FROM fts ORDER BY score DESC LIMIT $3
		), merged AS (
		  SELECT coalesce(v.source_type, f.source_type) AS source_type,
		         coalesce(v.source_id,   f.source_id)   AS source_id,
		         coalesce(v.content,     f.content)     AS content,
		         coalesce(v.parent,      f.parent)      AS parent,
		         coalesce(v.occurred_at, f.occurred_at) AS occurred_at,
		         v.vrank, f.frank
		    FROM vtop v FULL OUTER JOIN ftop f
		      ON v.source_type = f.source_type AND v.source_id = f.source_id
		)
		SELECT m.source_type, m.source_id,
		       coalesce(n.title, ms.subject, ev.title, '') AS title,
		       m.content, m.parent, m.occurred_at, coalesce(n.slug,''),
		       coalesce(m.vrank, 0), coalesce(m.frank, 0)
		  FROM merged m
		  LEFT JOIN notes    n  ON m.source_type='note'    AND n.id  = m.source_id
		  LEFT JOIN messages ms ON m.source_type='message' AND ms.id = m.source_id
		  LEFT JOIN events   ev ON m.source_type='event'   AND ev.id = m.source_id`,
		q, orTSQuery(query), depth, nilIfEmpty(types), fanout(depth))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var cands []scored
	for rows.Next() {
		var c scored
		if err := rows.Scan(&c.SourceType, &c.SourceID, &c.Title, &c.Snippet,
			&c.Parent, &c.OccurredAt, &c.Slug, &c.vecRank, &c.ftsRank); err != nil {
			return nil, err
		}
		if c.vecRank > 0 {
			c.rrf += 1 / (rrfK + float64(c.vecRank))
		}
		if c.ftsRank > 0 {
			c.rrf += 1 / (rrfK + float64(c.ftsRank))
		}
		c.Score = c.rrf
		cands = append(cands, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	sortByRRF(cands)
	out := make([]Hit, 0, limit)
	for i, c := range cands {
		if i >= depth {
			break
		}
		c.Hit.Snippet = squeeze(c.Hit.Snippet, 400)
		out = append(out, c.Hit)
	}
	return out, nil
}

func sortByRRF(c []scored) {
	for i := 1; i < len(c); i++ {
		for j := i; j > 0 && c[j].rrf > c[j-1].rrf; j-- {
			c[j], c[j-1] = c[j-1], c[j]
		}
	}
}

// ---------------------------------------------------------------- rerank

// Rerank reorders candidates with a cross-encoder.
//
// This is the single largest quality lever in the pipeline and the one most
// often skipped. Retrieval scores a query against a document independently;
// a cross-encoder reads them together, which is what lets it tell that "free
// food on campus" is about the mess menu and not about meeting your proctor.
func (s *Service) Rerank(ctx context.Context, query string, hits []Hit, top int) ([]Hit, error) {
	if len(hits) <= 1 || !s.Enabled() {
		return hits, nil
	}
	if top <= 0 || top > len(hits) {
		top = len(hits)
	}
	docs := make([]string, len(hits))
	for i, h := range hits {
		// Feed the section the chunk came from, not the matched fragment and
		// not the whole source. A cross-encoder reads query and document
		// together, so a bare fragment tells it nothing -- but a 9kB forwarded
		// thread buries the relevant lines, which is exactly how this ranked a
		// cultural festival above the mess menu for "free food". The section is
		// the span that is both self-contained and actually about one thing.
		body := h.Parent
		if strings.TrimSpace(body) == "" {
			body = h.Snippet
		}
		docs[i] = trunc(strings.TrimSpace(h.Title+"\n"+squeeze(body, 1400)), 1600)
	}
	body, _ := json.Marshal(map[string]any{
		"model":            rerankModel,
		"query":            query,
		"documents":        docs,
		"top_n":            top,
		"return_documents": false,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://openrouter.ai/api/v1/rerank", bytes.NewReader(body))
	if err != nil {
		return hits, err
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.http.Do(req)
	if err != nil {
		return hits, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return hits, fmt.Errorf("rerank %d: %s", resp.StatusCode, trunc(string(raw), 160))
	}
	var env struct {
		Results []struct {
			Index int     `json:"index"`
			Score float64 `json:"relevance_score"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return hits, err
	}
	if len(env.Results) == 0 {
		return hits, nil
	}
	out := make([]Hit, 0, len(env.Results))
	for _, r := range env.Results {
		if r.Index < 0 || r.Index >= len(hits) {
			continue
		}
		h := hits[r.Index]
		h.Score = r.Score
		out = append(out, h)
	}
	return out, nil
}

const rerankModel = "cohere/rerank-v3.5"

// Retrieve is the full pipeline: hybrid, then cross-encoder, then trim.
//
// Reranking failure is not fatal -- fused results are still good, and a search
// that degrades is better than one that errors.
func (s *Service) Retrieve(ctx context.Context, query string, limit int, types []string, rerank bool) ([]Hit, error) {
	hits, err := s.Hybrid(ctx, query, limit, types)
	if err != nil {
		return nil, err
	}
	if rerank && len(hits) > 1 {
		rctx, cancel := context.WithTimeout(ctx, 12*time.Second)
		defer cancel()
		if rr, err := s.Rerank(rctx, query, hits, limit); err == nil {
			hits = rr
		}
	}
	if len(hits) > limit {
		hits = hits[:limit]
	}
	for i := range hits {
		hits[i].Snippet = squeeze(hits[i].Snippet, 220)
	}
	return hits, nil
}

var _ = uuid.Nil
