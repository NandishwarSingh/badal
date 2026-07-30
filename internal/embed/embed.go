// Package embed builds and queries the retrieval index.
//
// Embeddings come from OpenRouter's /embeddings endpoint -- which exists even
// though embedding models are absent from its model list -- using
// text-embedding-3-small truncated to 768 dimensions.
//
// The truncation is Matryoshka, not naive slicing: the model is trained so that
// prefixes of the vector remain meaningful. Measured on this corpus, 768d
// separated related from unrelated text as well as the full 1536d
// (0.485/0.19 versus 0.477/0.175), at half the storage.
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	Dims       = 768
	DefaultMod = "openai/text-embedding-3-small"

	// Chunking. A1 splits on paragraphs with overlap; A3 replaces this with
	// structure-aware splitting that understands headings and quote levels.
	targetChars  = 1100
	overlapChars = 180
	minChars     = 60
	maxChunks    = 40
)

type Service struct {
	pool     *pgxpool.Pool
	apiKey   string
	model    string
	http     *http.Client
	endpoint string
}

func New(pool *pgxpool.Pool, apiKey, model string) *Service {
	if model == "" {
		model = DefaultMod
	}
	return &Service{
		pool: pool, apiKey: apiKey, model: model,
		http:     &http.Client{Timeout: 90 * time.Second},
		endpoint: "https://openrouter.ai/api/v1/embeddings",
	}
}

func (s *Service) Enabled() bool { return s.apiKey != "" }
func (s *Service) Model() string { return s.model }

// ------------------------------------------------------------------ vectors

// Embed returns one vector per input. Batched: the per-request overhead
// dominates for short texts, and a backfill of thousands of chunks one at a
// time would take hours of pure latency.
func (s *Service) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if !s.Enabled() {
		return nil, fmt.Errorf("no api key")
	}
	if len(texts) == 0 {
		return nil, nil
	}
	body, _ := json.Marshal(map[string]any{
		"model":      s.model,
		"input":      texts,
		"dimensions": Dims,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("HTTP-Referer", "https://badal.nandishwarsingh.com")
	req.Header.Set("X-Title", "badal")

	resp, err := s.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embeddings %d: %s", resp.StatusCode, trunc(string(raw), 200))
	}
	var env struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, err
	}
	if env.Error != nil {
		return nil, fmt.Errorf("embeddings: %s", env.Error.Message)
	}
	// The API may return out of order; index is authoritative.
	out := make([][]float32, len(texts))
	for _, d := range env.Data {
		if d.Index >= 0 && d.Index < len(out) {
			out[d.Index] = d.Embedding
		}
	}
	for i, v := range out {
		if len(v) != Dims {
			return nil, fmt.Errorf("input %d: got %d dims, want %d", i, len(v), Dims)
		}
	}
	return out, nil
}

// ----------------------------------------------------------------- chunking

// Chunk splits text on paragraph boundaries, packing to roughly targetChars
// with overlap so a fact spanning a boundary is not lost to both sides.
func Chunk(text string) []string {
	text = strings.TrimSpace(collapseBlank(text))
	if text == "" {
		return nil
	}
	if len(text) <= targetChars {
		return []string{text}
	}
	paras := strings.Split(text, "\n\n")
	var chunks []string
	var cur strings.Builder
	flush := func() {
		if s := strings.TrimSpace(cur.String()); len(s) >= minChars {
			chunks = append(chunks, s)
		}
		cur.Reset()
	}
	for _, p := range paras {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// A single paragraph larger than the target is split on sentences.
		if len(p) > targetChars {
			flush()
			for _, s := range splitLong(p) {
				chunks = append(chunks, s)
			}
			continue
		}
		if cur.Len()+len(p)+2 > targetChars {
			prev := cur.String()
			flush()
			// Carry the tail of the previous chunk forward.
			if len(prev) > overlapChars {
				cur.WriteString(strings.TrimSpace(prev[len(prev)-overlapChars:]))
				cur.WriteString("\n\n")
			}
		}
		cur.WriteString(p)
		cur.WriteString("\n\n")
	}
	flush()
	if len(chunks) > maxChunks {
		chunks = chunks[:maxChunks]
	}
	return chunks
}

func splitLong(p string) []string {
	var out []string
	var cur strings.Builder
	for _, sent := range strings.SplitAfter(p, ". ") {
		if cur.Len()+len(sent) > targetChars && cur.Len() > 0 {
			out = append(out, strings.TrimSpace(cur.String()))
			cur.Reset()
		}
		cur.WriteString(sent)
	}
	if s := strings.TrimSpace(cur.String()); len(s) >= minChars {
		out = append(out, s)
	}
	return out
}

func collapseBlank(s string) string {
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return s
}

// --------------------------------------------------------------- indexing

type Item struct {
	SourceType string
	SourceID   uuid.UUID
	Title      string
	Body       string
	OccurredAt *time.Time
}

// Index embeds one item and replaces its rows atomically. Delete-then-insert
// inside a transaction means a re-index can never leave a source half-covered
// by stale and fresh chunks at once.
func (s *Service) Index(ctx context.Context, it Item) (int, error) {
	full := strings.TrimSpace(it.Title + "\n\n" + it.Body)
	chunks := Chunk(full)
	if len(chunks) == 0 {
		return 0, nil
	}
	// The title rides along with every chunk: a paragraph deep inside an email
	// is far more findable when it carries what the email was about.
	inputs := make([]string, len(chunks))
	for i, c := range chunks {
		if it.Title != "" {
			inputs[i] = it.Title + "\n" + c
		} else {
			inputs[i] = c
		}
	}
	vecs, err := s.Embed(ctx, inputs)
	if err != nil {
		return 0, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`DELETE FROM embeddings WHERE source_type=$1 AND source_id=$2`,
		it.SourceType, it.SourceID); err != nil {
		return 0, err
	}
	for i, c := range chunks {
		if _, err := tx.Exec(ctx,
			`INSERT INTO embeddings
			   (source_type, source_id, chunk_index, content, parent, embedding, occurred_at, model)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
			it.SourceType, it.SourceID, i, c, full, vecString(vecs[i]), it.OccurredAt, s.model); err != nil {
			return 0, err
		}
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM embed_queue WHERE source_type=$1 AND source_id=$2`,
		it.SourceType, it.SourceID); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(chunks), nil
}

// vecString formats a vector for pgvector. Building the literal by hand beats
// a driver round trip for a type pgx does not know natively.
func vecString(v []float32) string {
	var b strings.Builder
	b.Grow(len(v) * 9)
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(f), 'g', 6, 32))
	}
	b.WriteByte(']')
	return b.String()
}

// ---------------------------------------------------------------- backfill

// Pending returns sources that have no current embedding.
func (s *Service) Pending(ctx context.Context, limit int) ([]Item, error) {
	if limit <= 0 || limit > 200 {
		limit = 25
	}
	rows, err := s.pool.Query(ctx, `
		SELECT 'note', n.id, n.title, n.body, n.updated_at
		  FROM notes n
		 WHERE n.deleted_at IS NULL
		   AND NOT EXISTS (SELECT 1 FROM embeddings e WHERE e.source_type='note' AND e.source_id=n.id)
		UNION ALL
		SELECT 'message', m.id, m.subject, m.body_text, m.received_at
		  FROM messages m
		 WHERE NOT EXISTS (SELECT 1 FROM embeddings e WHERE e.source_type='message' AND e.source_id=m.id)
		UNION ALL
		SELECT 'event', v.id, v.title, coalesce(v.notes,'') || ' ' || coalesce(v.location,'') || ' ' || coalesce(v.evidence,''), v.starts_at
		  FROM events v
		 WHERE v.deleted_at IS NULL
		   AND NOT EXISTS (SELECT 1 FROM embeddings e WHERE e.source_type='event' AND e.source_id=v.id)
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Item{}
	for rows.Next() {
		var it Item
		var when *time.Time
		if err := rows.Scan(&it.SourceType, &it.SourceID, &it.Title, &it.Body, &when); err != nil {
			return nil, err
		}
		it.OccurredAt = when
		out = append(out, it)
	}
	return out, rows.Err()
}

type BackfillResult struct {
	Items  int `json:"items"`
	Chunks int `json:"chunks"`
	Failed int `json:"failed"`
}

func (s *Service) Backfill(ctx context.Context, limit int) (BackfillResult, error) {
	var r BackfillResult
	items, err := s.Pending(ctx, limit)
	if err != nil {
		return r, err
	}
	for _, it := range items {
		n, err := s.Index(ctx, it)
		if err != nil {
			slog.Warn("embed", "type", it.SourceType, "id", it.SourceID, "err", err)
			r.Failed++
			continue
		}
		r.Items++
		r.Chunks += n
	}
	return r, nil
}

// ------------------------------------------------------------------ search

type Hit struct {
	// Full text of the source, carried for reranking only and stripped before
	// the response. A cross-encoder judges query against document, so feeding
	// it a 400-char window of boilerplate headers is what makes it wrong.
	Parent     string     `json:"-"`
	SourceType string     `json:"source_type"`
	SourceID   uuid.UUID  `json:"source_id"`
	Title      string     `json:"title"`
	Snippet    string     `json:"snippet"`
	Score      float64    `json:"score"`
	OccurredAt *time.Time `json:"occurred_at,omitempty"`
	Slug       string     `json:"slug,omitempty"`
}

// Semantic returns nearest neighbours, best chunk per source.
//
// DISTINCT ON collapses chunks to one row per source: without it a long email
// with five matching paragraphs would fill the whole result page by itself.
func (s *Service) Semantic(ctx context.Context, query string, limit int, types []string) ([]Hit, error) {
	if strings.TrimSpace(query) == "" {
		return []Hit{}, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	vecs, err := s.Embed(ctx, []string{query})
	if err != nil {
		return nil, err
	}
	q := vecString(vecs[0])

	rows, err := s.pool.Query(ctx, `
		WITH best AS (
		  SELECT DISTINCT ON (e.source_type, e.source_id)
		         e.source_type, e.source_id, e.content, e.occurred_at,
		         1 - (e.embedding <=> $1::halfvec) AS score
		    FROM embeddings e
		   WHERE ($3::text[] IS NULL OR e.source_type = ANY($3))
		   ORDER BY e.source_type, e.source_id, e.embedding <=> $1::halfvec
		)
		SELECT b.source_type, b.source_id,
		       coalesce(n.title, m.subject, v.title, '') AS title,
		       b.content, b.score, b.occurred_at, coalesce(n.slug,'')
		  FROM best b
		  LEFT JOIN notes    n ON b.source_type='note'    AND n.id = b.source_id
		  LEFT JOIN messages m ON b.source_type='message' AND m.id = b.source_id
		  LEFT JOIN events   v ON b.source_type='event'   AND v.id = b.source_id
		 ORDER BY b.score DESC
		 LIMIT $2`, q, limit, nilIfEmpty(types))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Hit{}
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.SourceType, &h.SourceID, &h.Title, &h.Snippet,
			&h.Score, &h.OccurredAt, &h.Slug); err != nil {
			return nil, err
		}
		h.Snippet = squeeze(h.Snippet, 220)
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *Service) Stats(ctx context.Context) (map[string]any, error) {
	out := map[string]any{}
	rows, err := s.pool.Query(ctx,
		`SELECT source_type, count(DISTINCT source_id), count(*)
		   FROM embeddings GROUP BY source_type`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	by := map[string]any{}
	total := 0
	for rows.Next() {
		var t string
		var srcs, chunks int
		if err := rows.Scan(&t, &srcs, &chunks); err != nil {
			return nil, err
		}
		by[t] = map[string]int{"sources": srcs, "chunks": chunks}
		total += chunks
	}
	out["by_type"] = by
	out["chunks"] = total
	out["model"] = s.model
	out["dims"] = Dims
	return out, nil
}

func nilIfEmpty(v []string) any {
	if len(v) == 0 {
		return nil
	}
	return v
}

func squeeze(s string, n int) string {
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool {
		return unicode.IsSpace(r)
	}), " ")
	return trunc(s, n)
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
