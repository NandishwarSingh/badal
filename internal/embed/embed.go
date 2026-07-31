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
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	Dims       = 768
	DefaultMod = "openai/text-embedding-3-small"

	// How deep the HNSW scan goes before chunks are collapsed to one row per
	// source. Several chunks of the same document routinely rank together, so
	// the neighbour list has to be a good deal longer than the result page or
	// the tail of it is all duplicates of the top hit.
	knnFanout = 12
	minFanout = 200

	// ef_search trades recall for latency inside the HNSW graph. The default of
	// 40 is below the fan-out above, which would quietly cap how many distinct
	// documents a search can ever see.
	efSearch = 320
)

type Service struct {
	pool     *pgxpool.Pool
	apiKey   string
	model    string
	http     *http.Client
	endpoint string
	busy     atomic.Bool
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
	pieces := Split(it.Title, it.Body)
	if len(pieces) == 0 {
		return 0, nil
	}
	// The title and the section heading ride along with every chunk: a bullet
	// deep inside an email is far more findable when it carries what the email
	// was about and which part of it this came from. Both are prepended to the
	// embedding input only -- the stored content stays verbatim, so snippets
	// show the passage rather than a synthesised header.
	inputs := make([]string, len(pieces))
	for i, p := range pieces {
		inputs[i] = strings.TrimSpace(
			strings.TrimSpace(it.Title+"\n"+p.Head) + "\n" + p.Text)
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
	for i, p := range pieces {
		if _, err := tx.Exec(ctx,
			`INSERT INTO embeddings
			   (source_type, source_id, chunk_index, content, parent, embedding, occurred_at, model)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
			it.SourceType, it.SourceID, i, p.Text, p.Parent, vecString(vecs[i]),
			it.OccurredAt, s.model); err != nil {
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
	return len(pieces), nil
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
		UNION ALL
		SELECT 'task', k.id, k.title, k.notes, coalesce(k.due_at, k.created_at)
		  FROM tasks k
		 WHERE k.deleted_at IS NULL
		   AND NOT EXISTS (SELECT 1 FROM embeddings e WHERE e.source_type='task' AND e.source_id=k.id)
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

// ErrBusy means a rebuild is already running.
var ErrBusy = errors.New("reindex already running")

// Reindex drops every chunk and rebuilds from source.
//
// A chunking change is not a schema migration: the old rows stay perfectly
// valid-looking, so nothing will ever notice them and the new splitter only
// applies to mail that happens to arrive later. Adopting it means rebuilding
// the lot.
//
// It runs detached from the request. A full rebuild is minutes of sequential
// embedding calls on a one-core box, which outlives any sane HTTP timeout;
// progress shows up in /v1/embeddings/stats.
func (s *Service) Reindex(ctx context.Context) error {
	if !s.Enabled() {
		return fmt.Errorf("no api key")
	}
	if !s.busy.CompareAndSwap(false, true) {
		return ErrBusy
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM embeddings`); err != nil {
		s.busy.Store(false)
		return err
	}
	go func() {
		defer s.busy.Store(false)
		bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Minute)
		defer cancel()
		total := 0
		for {
			res, err := s.Backfill(bg, 25)
			if err != nil {
				slog.Error("reindex", "err", err, "done", total)
				return
			}
			// No progress means what is left cannot be embedded; looping on it
			// would spin forever against the same failures.
			if res.Items == 0 {
				slog.Info("reindex complete", "chunks", total, "stuck", res.Failed)
				return
			}
			total += res.Chunks
		}
	}()
	return nil
}

// Rebuilding reports whether a rebuild is in flight.
func (s *Service) Rebuilding() bool { return s.busy.Load() }

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
	Subjects   []string   `json:"subjects,omitempty"`
}

// Semantic returns nearest neighbours, best chunk per source.
//
// DISTINCT ON collapses chunks to one row per source: without it a long email
// with five matching paragraphs would fill the whole result page by itself.
func (s *Service) Semantic(ctx context.Context, query string, limit int, types, subs []string) ([]Hit, error) {
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

	tx, err := s.beginSearch(ctx, len(subs) > 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// The ORDER BY / LIMIT sits on the bare table so HNSW can serve it. Folding
	// DISTINCT ON into this level, as the first version did, forced a sort over
	// every row in the table and the index went untouched on every search.
	rows, err := tx.Query(ctx, `
		WITH knn AS (
		  SELECT source_type, source_id, content, occurred_at,
		         embedding <=> $1::halfvec AS dist
		    FROM embeddings
		   WHERE ($3::text[] IS NULL OR source_type = ANY($3))
		     AND ($5::text[] IS NULL OR subjects && $5)
		   ORDER BY embedding <=> $1::halfvec
		   LIMIT $4
		), best AS (
		  SELECT DISTINCT ON (source_type, source_id)
		         source_type, source_id, content, occurred_at, 1 - dist AS score
		    FROM knn
		   ORDER BY source_type, source_id, dist
		)
		SELECT b.source_type, b.source_id,
		       coalesce(n.title, m.subject, v.title, k.title, '') AS title,
		       b.content, b.score, b.occurred_at, coalesce(n.slug,''),
		       coalesce(sj.subs, '{}')
		  FROM best b
		  LEFT JOIN notes    n ON b.source_type='note'    AND n.id = b.source_id
		  LEFT JOIN messages m ON b.source_type='message' AND m.id = b.source_id
		  LEFT JOIN events   v ON b.source_type='event'   AND v.id = b.source_id
		  LEFT JOIN tasks    k ON b.source_type='task'    AND k.id = b.source_id
		  LEFT JOIN LATERAL (
		    SELECT array_agg(ss.slug ORDER BY ss.confidence DESC) AS subs
		      FROM source_subjects ss
		     WHERE ss.source_type = b.source_type AND ss.source_id = b.source_id
		  ) sj ON true
		 ORDER BY b.score DESC
		 LIMIT $2`, q, limit, nilIfEmpty(types), fanout(limit), nilIfEmpty(subs))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Hit{}
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.SourceType, &h.SourceID, &h.Title, &h.Snippet,
			&h.Score, &h.OccurredAt, &h.Slug, &h.Subjects); err != nil {
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

// fanout is how many chunk neighbours to pull before collapsing per source.
func fanout(limit int) int {
	n := limit * knnFanout
	if n < minFanout {
		n = minFanout
	}
	return n
}

// beginSearch opens a read-only transaction with ef_search raised. It has to be
// a transaction: SET LOCAL is the only form that cannot leak the setting onto
// the next borrower of a pooled connection.
//
// filtered turns on iterative scan. An HNSW scan finds neighbours first and
// applies the WHERE clause afterwards, so a narrow filter -- "only Internships"
// -- can return almost nothing while plenty of matches exist just past the
// fixed candidate window. Iterative scan keeps pulling batches until enough
// rows survive the filter. relaxed_order lets it do that without re-sorting
// each batch into strict distance order, which costs a little ordering
// precision that fusion and reranking then wash out anyway.
func (s *Service) beginSearch(ctx context.Context, filtered bool) (pgx.Tx, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	set := `SET LOCAL hnsw.ef_search = ` + strconv.Itoa(efSearch)
	if filtered {
		set += `; SET LOCAL hnsw.iterative_scan = relaxed_order`
	}
	if _, err := tx.Exec(ctx, set); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
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
