// Package subjects auto-tags notes, mail and events against a fixed taxonomy.
//
// The taxonomy is closed on purpose. Free-form tagging on this corpus produces
// a long tail of near-duplicates -- "Internship", "internships", "Internship
// Opportunity" -- and a filter built on that is worse than no filter, because
// picking one of the three silently hides the other two. Here the vocabulary
// comes from the subjects table and is compiled into the model's JSON schema as
// an enum, so an invalid tag cannot be returned at all.
//
// Tags are advisory, not structural: nothing else keys off them, so a wrong tag
// costs a misplaced item in a filter and never a lost one.
package subjects

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Version is stamped on every run. Bump it when the prompt or taxonomy changes
// so a retag can tell old rows from new ones.
const Version = 1

const (
	maxBodyChars = 6000
	maxTags      = 3
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
		model = "deepseek/deepseek-v4-flash"
	}
	return &Service{
		pool: pool, apiKey: apiKey, model: model,
		http:     &http.Client{Timeout: 90 * time.Second},
		endpoint: "https://openrouter.ai/api/v1/chat/completions",
	}
}

func (s *Service) Enabled() bool { return s.apiKey != "" }
func (s *Service) Tagging() bool { return s.busy.Load() }
func (s *Service) Model() string { return s.model }

// ---------------------------------------------------------------- taxonomy

type Subject struct {
	Slug  string `json:"slug"`
	Label string `json:"label"`
	Kind  string `json:"kind"`
	Count int    `json:"count"`
}

// List returns the taxonomy with how many sources carry each subject. Subjects
// with nothing in them are still returned: an empty filter chip is information
// ("nothing here yet"), and hiding it makes the row jump around as mail lands.
func (s *Service) List(ctx context.Context) ([]Subject, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT s.slug, s.label, s.kind, count(ss.source_id)
		  FROM subjects s
		  LEFT JOIN source_subjects ss ON ss.slug = s.slug
		 GROUP BY s.slug, s.label, s.kind, s.sort
		 ORDER BY s.sort, s.label`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Subject{}
	for rows.Next() {
		var v Subject
		if err := rows.Scan(&v.Slug, &v.Label, &v.Kind, &v.Count); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Service) slugs(ctx context.Context) ([]string, map[string]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT slug, label FROM subjects ORDER BY sort, label`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var list []string
	labels := map[string]string{}
	for rows.Next() {
		var slug, label string
		if err := rows.Scan(&slug, &label); err != nil {
			return nil, nil, err
		}
		list = append(list, slug)
		labels[slug] = label
	}
	return list, labels, rows.Err()
}

// ------------------------------------------------------------------ model

type Item struct {
	SourceType string
	SourceID   uuid.UUID
	Title      string
	Body       string
}

type tagged struct {
	Subjects []struct {
		Slug       string  `json:"slug"`
		Confidence float64 `json:"confidence"`
	} `json:"subjects"`
}

const systemPrompt = `You tag university items with subjects from a fixed list.

Rules:
- Choose only from the allowed slugs. Never invent one.
- Return at most 3, ordered most relevant first. Usually 1 or 2 is right.
- Tag what the item IS ABOUT, not who sent it. A circular from the hostel
  office about exam dates is exams, not hostel.
- confidence 0..1. Below 0.5 means you are guessing; return fewer tags instead.
- Return an empty list rather than forcing a tag onto something that fits none.

Prefer one confident tag over three vague ones.`

type chatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (s *Service) call(ctx context.Context, user string, allowed []string) (tagged, error) {
	var out tagged

	// The allowed slugs become an enum, so the model cannot return a tag that
	// is not in the taxonomy -- the constraint is structural, not a request.
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"subjects": map[string]any{
				"type":     "array",
				"maxItems": maxTags,
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"slug":       map[string]any{"type": "string", "enum": allowed},
						"confidence": map[string]any{"type": "number"},
					},
					"required":             []string{"slug", "confidence"},
					"additionalProperties": false,
				},
			},
		},
		"required":             []string{"subjects"},
		"additionalProperties": false,
	}
	rf, _ := json.Marshal(map[string]any{
		"type": "json_schema",
		"json_schema": map[string]any{
			"name": "tagging", "strict": true, "schema": schema,
		},
	})
	payload, _ := json.Marshal(map[string]any{
		"model": s.model,
		"messages": []chatMsg{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: user},
		},
		"temperature":     0,
		"response_format": json.RawMessage(rf),
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(payload))
	if err != nil {
		return out, err
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("HTTP-Referer", "https://badal.nandishwarsingh.com")
	req.Header.Set("X-Title", "badal")

	resp, err := s.http.Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("openrouter %d: %s", resp.StatusCode, trim(string(raw), 240))
	}
	var env struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return out, err
	}
	if env.Error != nil {
		return out, fmt.Errorf("openrouter: %s", env.Error.Message)
	}
	if len(env.Choices) == 0 {
		return out, fmt.Errorf("no choices")
	}
	if err := json.Unmarshal([]byte(env.Choices[0].Message.Content), &out); err != nil {
		return out, fmt.Errorf("bad json: %w", err)
	}
	return out, nil
}

// ------------------------------------------------------------------ tagging

// Tag classifies one item and replaces its tags.
func (s *Service) Tag(ctx context.Context, it Item) ([]string, error) {
	allowed, labels, err := s.slugs(ctx)
	if err != nil {
		return nil, err
	}
	if len(allowed) == 0 {
		return nil, fmt.Errorf("taxonomy is empty")
	}

	var b strings.Builder
	b.WriteString("Allowed subjects:\n")
	for _, sl := range allowed {
		fmt.Fprintf(&b, "- %s (%s)\n", sl, labels[sl])
	}
	fmt.Fprintf(&b, "\nType: %s\nTitle: %s\n\n%s", it.SourceType, it.Title, trim(it.Body, maxBodyChars))

	res, err := s.call(ctx, b.String(), allowed)
	if err != nil {
		return nil, err
	}

	valid := map[string]bool{}
	for _, sl := range allowed {
		valid[sl] = true
	}
	type pick struct {
		slug string
		conf float64
	}
	var picks []pick
	seen := map[string]bool{}
	for _, sub := range res.Subjects {
		// The enum should make this unreachable; checked anyway, because a
		// schema the provider silently ignores would otherwise write a slug
		// that violates the foreign key and fail the whole transaction.
		if !valid[sub.Slug] || seen[sub.Slug] || sub.Confidence < 0.5 {
			continue
		}
		seen[sub.Slug] = true
		picks = append(picks, pick{sub.Slug, sub.Confidence})
		if len(picks) == maxTags {
			break
		}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`DELETE FROM source_subjects WHERE source_type=$1 AND source_id=$2`,
		it.SourceType, it.SourceID); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(picks))
	for _, p := range picks {
		if _, err := tx.Exec(ctx,
			`INSERT INTO source_subjects (source_type, source_id, slug, confidence)
			 VALUES ($1,$2,$3,$4)`,
			it.SourceType, it.SourceID, p.slug, p.conf); err != nil {
			return nil, err
		}
		out = append(out, p.slug)
	}
	// Mirror onto the chunks so vector search can filter without a join.
	if _, err := tx.Exec(ctx,
		`UPDATE embeddings SET subjects=$3 WHERE source_type=$1 AND source_id=$2`,
		it.SourceType, it.SourceID, out); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO subject_runs (source_type, source_id, version, model)
		 VALUES ($1,$2,$3,$4)
		 ON CONFLICT (source_type, source_id)
		 DO UPDATE SET version=EXCLUDED.version, model=EXCLUDED.model, tagged_at=now()`,
		it.SourceType, it.SourceID, Version, s.model); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

// Pending returns sources the tagger has not looked at under this version.
func (s *Service) Pending(ctx context.Context, limit int) ([]Item, error) {
	if limit <= 0 || limit > 200 {
		limit = 25
	}
	rows, err := s.pool.Query(ctx, `
		WITH todo AS (
		  SELECT 'note' AS t, n.id, n.title, n.body AS body, n.updated_at AS at
		    FROM notes n WHERE n.deleted_at IS NULL
		  UNION ALL
		  SELECT 'message', m.id, m.subject, m.body_text, m.received_at FROM messages m
		  UNION ALL
		  SELECT 'task', k.id, k.title, k.notes, coalesce(k.due_at, k.created_at)
		    FROM tasks k WHERE k.deleted_at IS NULL
		  UNION ALL
		  SELECT 'event', v.id, v.title,
		         coalesce(v.notes,'') || ' ' || coalesce(v.location,'') || ' ' || coalesce(v.evidence,''),
		         v.starts_at
		    FROM events v WHERE v.deleted_at IS NULL
		)
		SELECT t, id, title, body FROM todo
		 WHERE NOT EXISTS (
		   SELECT 1 FROM subject_runs r
		    WHERE r.source_type = todo.t AND r.source_id = todo.id AND r.version = $2)
		 ORDER BY at DESC
		 LIMIT $1`, limit, Version)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Item{}
	for rows.Next() {
		var it Item
		if err := rows.Scan(&it.SourceType, &it.SourceID, &it.Title, &it.Body); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

type Result struct {
	Items  int `json:"items"`
	Tags   int `json:"tags"`
	Failed int `json:"failed"`
}

func (s *Service) Backfill(ctx context.Context, limit int) (Result, error) {
	var r Result
	items, err := s.Pending(ctx, limit)
	if err != nil {
		return r, err
	}
	for _, it := range items {
		tags, err := s.Tag(ctx, it)
		if err != nil {
			slog.Warn("tag", "type", it.SourceType, "id", it.SourceID, "err", err)
			r.Failed++
			continue
		}
		r.Items++
		r.Tags += len(tags)
	}
	return r, nil
}

// Run tags everything outstanding, detached from the request. Same shape as the
// embedding rebuild: a full pass is minutes of sequential model calls.
func (s *Service) Run(ctx context.Context, retag bool) error {
	if !s.Enabled() {
		return fmt.Errorf("no api key")
	}
	if !s.busy.CompareAndSwap(false, true) {
		return ErrBusy
	}
	if retag {
		if _, err := s.pool.Exec(ctx, `DELETE FROM subject_runs`); err != nil {
			s.busy.Store(false)
			return err
		}
	}
	go func() {
		defer s.busy.Store(false)
		bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Minute)
		defer cancel()
		total := 0
		for {
			res, err := s.Backfill(bg, 20)
			if err != nil {
				slog.Error("subject run", "err", err, "done", total)
				return
			}
			// No progress means the remainder cannot be tagged; looping would
			// spin on the same failures forever.
			if res.Items == 0 {
				slog.Info("subject run complete", "items", total, "stuck", res.Failed)
				return
			}
			total += res.Items
		}
	}()
	return nil
}

var ErrBusy = fmt.Errorf("tagging already running")

func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
