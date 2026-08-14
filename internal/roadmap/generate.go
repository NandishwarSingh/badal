package roadmap

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

const (
	maxChunkChars = 8000
	maxTotalChars = 64000 // ~8 chunks; bounds cost and time on the 1-vCPU VPS
)

// genItem is one line the model returns: a flat, depth-tagged outline.
type genItem struct {
	Title string `json:"title"`
	Kind  string `json:"kind"`  // topic | leaf
	Depth int    `json:"depth"` // 0 unit, 1 topic, 2 subtopic/leaf
}

const sysPrompt = `You convert a university course's study material into an
exhaustive revision checklist.

Output a FLAT, ORDERED outline as "items", each with:
- depth: 0 for a module/unit, 1 for a topic, 2 for a subtopic or specific item
- kind: "topic" for a heading that groups things below it, "leaf" for a concrete
  thing to study, revise, derive or practise
- title: short and specific

Rules:
- COVER EVERYTHING. Every heading, definition, theorem, algorithm, formula,
  method and named concept in the text becomes an item. Never summarise detail
  away or merge distinct topics.
- Leaves must be specific and checkable ("Derive the normal equations for
  linear regression"), not vague ("understand regression").
- Preserve the order of the material.
- Use only what is in the text. Do not invent topics that are not there.`

var genSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"items": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"title": map[string]any{"type": "string"},
					"kind":  map[string]any{"type": "string", "enum": []string{"topic", "leaf"}},
					"depth": map[string]any{"type": "integer"},
				},
				"required":             []string{"title", "kind", "depth"},
				"additionalProperties": false,
			},
		},
	},
	"required":             []string{"items"},
	"additionalProperties": false,
}

// Create runs the whole pipeline synchronously and stores the result.
func (s *Service) Create(ctx context.Context, device uuid.UUID, subject, title string, uploads []Upload) (Roadmap, error) {
	if strings.TrimSpace(title) == "" {
		title = subject
	}
	// The roadmap row exists immediately in 'processing', so a slow generation
	// still leaves something the client can show and poll.
	var id uuid.UUID
	hlc := s.feed.Clock.Now()
	if err := s.pool.QueryRow(ctx, `
		INSERT INTO roadmaps (subject, title, status, gen_version, hlc)
		VALUES ($1,$2,'processing',$3,$4) RETURNING id`,
		subject, title, GenVersion, hlc).Scan(&id); err != nil {
		return Roadmap{}, err
	}

	rm, genErr := s.generate(ctx, device, id, uploads)
	if genErr != nil {
		_, _ = s.pool.Exec(ctx,
			`UPDATE roadmaps SET status='failed', error=$2, updated_at=now() WHERE id=$1`,
			id, genErr.Error())
		return s.Get(ctx, id)
	}
	return rm, nil
}

func (s *Service) generate(ctx context.Context, device, id uuid.UUID, uploads []Upload) (Roadmap, error) {
	// 1. anydoc each upload to Markdown. Empty text means a scanned/image PDF
	//    with no text layer -- recorded, not silently dropped.
	var merged strings.Builder
	for _, u := range uploads {
		md, err := s.anydoc.ConvertBytes(ctx, u.Data, u.Ext)
		if err != nil {
			return Roadmap{}, fmt.Errorf("convert %s: %w", u.Filename, err)
		}
		_, _ = s.pool.Exec(ctx,
			`INSERT INTO roadmap_sources (roadmap_id, filename, bytes) VALUES ($1,$2,$3)`,
			id, u.Filename, len(u.Data))
		if strings.TrimSpace(md) == "" {
			continue // no text layer; nothing to add from this file
		}
		merged.WriteString("\n\n# " + u.Filename + "\n\n")
		merged.WriteString(md)
	}
	full := strings.TrimSpace(merged.String())
	if full == "" {
		return Roadmap{}, ErrNoDocs
	}
	if len(full) > maxTotalChars {
		full = full[:maxTotalChars]
	}

	// 2. Map: every chunk becomes items. The per-chunk "cover everything"
	//    instruction is the first line of the coverage guarantee.
	var items []genItem
	for _, chunk := range splitByHeading(full, maxChunkChars) {
		got, err := s.callMap(ctx, chunk)
		if err != nil {
			return Roadmap{}, err
		}
		items = append(items, got...)
	}
	items = dedupe(items)

	// 3. Coverage check, done mechanically so it does not depend on the model:
	//    any source heading not represented in an item title is appended.
	items = appendMissing(items, full)
	if len(items) == 0 {
		return Roadmap{}, ErrNoDocs
	}

	// 4. Persist the tree.
	if err := s.persist(ctx, device, id, items); err != nil {
		return Roadmap{}, err
	}
	return s.Get(ctx, id)
}

// persist assembles the flat depth list into parent-linked rows.
func (s *Service) persist(ctx context.Context, device, id uuid.UUID, items []genItem) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// A stack of the most recent id at each depth builds the parent links.
	var stack []uuid.UUID
	hlc := s.feed.Clock.Now()
	sort := 0
	for _, it := range items {
		d := it.Depth
		if d < 0 {
			d = 0
		}
		if d > 5 {
			d = 5
		}
		if len(stack) > d {
			stack = stack[:d]
		}
		var parent *uuid.UUID
		if len(stack) > 0 {
			p := stack[len(stack)-1]
			parent = &p
		}
		kind := it.Kind
		if kind != "topic" && kind != "leaf" {
			kind = "leaf"
		}
		var newID uuid.UUID
		if err := tx.QueryRow(ctx, `
			INSERT INTO roadmap_items
			  (roadmap_id, parent_id, title, depth, sort, kind, hlc)
			VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
			id, parent, trimTitle(it.Title), d, sort, kind, hlc).Scan(&newID); err != nil {
			return err
		}
		sort++
		// This node becomes the parent for the next deeper one.
		if len(stack) == d {
			stack = append(stack, newID)
		} else {
			stack = stack[:d]
			stack = append(stack, newID)
		}
	}
	if _, err := tx.Exec(ctx,
		`UPDATE roadmaps SET status='ready', item_count=$2, updated_at=now() WHERE id=$1`,
		id, len(items)); err != nil {
		return err
	}
	if _, err := s.feed.Append(ctx, tx, "roadmap", id, "upsert", device); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ------------------------------------------------------------ helpers

var headingRe = regexp.MustCompile(`(?m)^#{1,6}\s+(.+?)\s*$`)

// splitByHeading cuts the markdown into <=maxChars chunks at heading
// boundaries, so a topic is never split across two model calls.
func splitByHeading(md string, maxChars int) []string {
	locs := headingRe.FindAllStringIndex(md, -1)
	if len(locs) == 0 {
		return hardWrap(md, maxChars)
	}
	// Sections start at each heading.
	var sections []string
	for i, loc := range locs {
		end := len(md)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		sections = append(sections, md[loc[0]:end])
	}
	var chunks []string
	var cur strings.Builder
	for _, sec := range sections {
		if cur.Len() > 0 && cur.Len()+len(sec) > maxChars {
			chunks = append(chunks, cur.String())
			cur.Reset()
		}
		if len(sec) > maxChars {
			// A single huge section: hard-wrap it on its own.
			for _, w := range hardWrap(sec, maxChars) {
				chunks = append(chunks, w)
			}
			continue
		}
		cur.WriteString(sec)
	}
	if cur.Len() > 0 {
		chunks = append(chunks, cur.String())
	}
	return chunks
}

func hardWrap(s string, n int) []string {
	var out []string
	for len(s) > n {
		cut := strings.LastIndex(s[:n], "\n")
		if cut < n/2 {
			cut = n
		}
		out = append(out, s[:cut])
		s = s[cut:]
	}
	if strings.TrimSpace(s) != "" {
		out = append(out, s)
	}
	return out
}

func norm(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = regexp.MustCompile(`[^a-z0-9 ]+`).ReplaceAllString(s, "")
	return strings.Join(strings.Fields(s), " ")
}

// dedupe drops an item whose normalised title repeats one already kept at the
// same depth -- the same topic arriving from two PDFs.
func dedupe(items []genItem) []genItem {
	seen := map[string]bool{}
	out := items[:0:0]
	for _, it := range items {
		key := fmt.Sprintf("%d|%s", it.Depth, norm(it.Title))
		if norm(it.Title) == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, it)
	}
	return out
}

// appendMissing is the mechanical coverage guarantee: any source heading not
// already present as an item title is added, so the model cannot quietly drop a
// topic. Added items sit under one clearly labelled group.
func appendMissing(items []genItem, md string) []genItem {
	have := map[string]bool{}
	for _, it := range items {
		have[norm(it.Title)] = true
	}
	var missing []string
	for _, m := range headingRe.FindAllStringSubmatch(md, -1) {
		h := strings.TrimSpace(m[1])
		n := norm(h)
		if n == "" || have[n] {
			continue
		}
		// A heading is "covered" if any item title contains it or vice versa.
		covered := false
		for k := range have {
			if strings.Contains(k, n) || strings.Contains(n, k) {
				covered = true
				break
			}
		}
		if !covered {
			have[n] = true
			missing = append(missing, h)
		}
	}
	if len(missing) == 0 {
		return items
	}
	items = append(items, genItem{Title: "Also in the material", Kind: "topic", Depth: 0})
	for _, h := range missing {
		items = append(items, genItem{Title: h, Kind: "leaf", Depth: 1})
	}
	return items
}

func trimTitle(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// ------------------------------------------------------------- openrouter

type chatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (s *Service) callMap(ctx context.Context, chunk string) ([]genItem, error) {
	rf, _ := json.Marshal(map[string]any{
		"type":        "json_schema",
		"json_schema": map[string]any{"name": "outline", "strict": true, "schema": genSchema},
	})
	payload, _ := json.Marshal(map[string]any{
		"model": s.model,
		"messages": []chatMsg{
			{Role: "system", Content: sysPrompt},
			{Role: "user", Content: "MATERIAL:\n\n" + chunk},
		},
		"temperature":     0,
		"response_format": json.RawMessage(rf),
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(payload))
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
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openrouter %d: %s", resp.StatusCode, trunc(string(raw), 200))
	}
	var env struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &env); err != nil || len(env.Choices) == 0 {
		return nil, fmt.Errorf("bad openrouter response")
	}
	var parsed struct {
		Items []genItem `json:"items"`
	}
	if err := json.Unmarshal([]byte(env.Choices[0].Message.Content), &parsed); err != nil {
		return nil, fmt.Errorf("unparseable outline: %w", err)
	}
	return parsed.Items, nil
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
