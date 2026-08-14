package roadmap

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

const (
	maxTotalChars   = 200000 // cap the merged document we will parse
	maxSectionChars = 6000   // per-section body sent to the model
	maxLeafSections = 80     // bound the number of model calls
	maxItems        = 600    // bound total tree size
	genConcurrency  = 4      // parallel per-section extractions
)

// treeNode is the assembled roadmap before it hits the database. kind is never
// stored from here -- persistTree derives it from whether a node has children,
// which is the whole point of the redesign: a branch always has children, a
// leaf never does, so "empty dropdown" and "leaf hiding its children" cannot
// happen.
type treeNode struct {
	Title    string
	Children []*treeNode
}

// ------------------------------------------------------------ heading parse

var headingRe = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*$`)
var commentRe = regexp.MustCompile(`(?s)<!--.*?-->`)

// section is a node of the document's own heading hierarchy. This structure is
// deterministic -- it comes from the Markdown, not the model -- so it is always
// internally consistent.
type section struct {
	level    int
	title    string
	body     strings.Builder
	children []*section
}

// parseSections turns Markdown into its heading tree. A heading's parent is the
// nearest preceding heading of a lower level; text lives on the section it
// falls under. A synthetic level-0 root holds the top-level headings (and any
// preamble text for the no-heading fallback).
func parseSections(md string) *section {
	root := &section{level: 0}
	stack := []*section{root}
	for _, line := range strings.Split(md, "\n") {
		if m := headingRe.FindStringSubmatch(line); m != nil {
			lvl := len(m[1])
			node := &section{level: lvl, title: strings.TrimSpace(m[2])}
			for len(stack) > 1 && stack[len(stack)-1].level >= lvl {
				stack = stack[:len(stack)-1]
			}
			parent := stack[len(stack)-1]
			parent.children = append(parent.children, node)
			stack = append(stack, node)
			continue
		}
		stack[len(stack)-1].body.WriteString(line + "\n")
	}
	return root
}

func cleanBody(s string) string {
	s = commentRe.ReplaceAllString(s, "")
	s = strings.TrimSpace(s)
	if len(s) > maxSectionChars {
		s = s[:maxSectionChars]
	}
	return s
}

// meaningful decides whether a leaf section's body is worth a model call.
func meaningful(s string) bool {
	return len(strings.Fields(s)) >= 6
}

// ------------------------------------------------------------------ create

// Create runs the pipeline synchronously and stores the result. A recover
// guard and a background-context failure write mean a panic or a cancelled
// request can never strand the row in 'processing'.
func (s *Service) Create(ctx context.Context, device uuid.UUID, subject, title string, uploads []Upload) (Roadmap, error) {
	if strings.TrimSpace(title) == "" {
		title = subject
	}
	var id uuid.UUID
	hlc := s.feed.Clock.Now()
	if err := s.pool.QueryRow(ctx, `
		INSERT INTO roadmaps (subject, title, status, gen_version, hlc)
		VALUES ($1,$2,'processing',$3,$4) RETURNING id`,
		subject, title, GenVersion, hlc).Scan(&id); err != nil {
		return Roadmap{}, err
	}

	rm, genErr := s.safeGenerate(ctx, device, id, uploads)
	if genErr != nil {
		bg, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = s.pool.Exec(bg,
			`UPDATE roadmaps SET status='failed', error=$2, updated_at=now() WHERE id=$1`,
			id, genErr.Error())
		return s.Get(bg, id)
	}
	return rm, nil
}

func (s *Service) safeGenerate(ctx context.Context, device, id uuid.UUID, uploads []Upload) (rm Roadmap, err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("roadmap generation panicked", "id", id, "panic", r)
			err = fmt.Errorf("internal error while building the roadmap")
		}
	}()
	return s.generate(ctx, device, id, uploads)
}

func (s *Service) generate(ctx context.Context, device, id uuid.UUID, uploads []Upload) (Roadmap, error) {
	// 1. anydoc each upload to Markdown; record every source. Empty output is a
	//    scanned/image PDF with no text layer.
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
			continue
		}
		merged.WriteString("\n\n<!-- source: " + u.Filename + " -->\n\n")
		merged.WriteString(md)
	}
	full := strings.TrimSpace(merged.String())
	if full == "" {
		return Roadmap{}, ErrNoDocs
	}
	if len(full) > maxTotalChars {
		full = full[:maxTotalChars]
	}

	// 2. The document's heading hierarchy is the tree skeleton.
	root := parseSections(full)

	// 3. Turn each leaf section's body into checkable items (in parallel), then
	//    assemble. Structure comes from headings; the model only writes leaves.
	roots, total, err := s.assemble(ctx, root)
	if err != nil {
		return Roadmap{}, err
	}
	if total == 0 || len(roots) == 0 {
		return Roadmap{}, ErrNoDocs
	}

	// 4. Persist, deriving kind from structure.
	if err := s.persistTree(ctx, device, id, roots, total); err != nil {
		return Roadmap{}, err
	}
	return s.Get(ctx, id)
}

// assemble collects the leaf sections, extracts their items concurrently, and
// builds the tree. A section with sub-headings is a pure branch; a leaf section
// becomes (heading -> its items), or a single leaf if it has no usable items.
func (s *Service) assemble(ctx context.Context, root *section) ([]*treeNode, int, error) {
	var leaves []*section
	var collect func(*section)
	collect = func(sec *section) {
		if len(sec.children) == 0 {
			if meaningful(cleanBody(sec.body.String())) {
				leaves = append(leaves, sec)
			}
			return
		}
		for _, c := range sec.children {
			collect(c)
		}
	}
	noHeadings := len(root.children) == 0
	if noHeadings {
		// A document anydoc gave no headings for: treat the whole thing as one
		// section and produce a flat, complete list.
		if meaningful(cleanBody(root.body.String())) {
			leaves = []*section{root}
		}
	} else {
		for _, c := range root.children {
			collect(c)
		}
	}
	if len(leaves) > maxLeafSections {
		leaves = leaves[:maxLeafSections]
	}

	// Extract items for each leaf section, bounded concurrency. A section that
	// fails is left without items -- it degrades to a single leaf, never a lost
	// roadmap.
	itemsBy := make(map[*section][]string, len(leaves))
	var mu sync.Mutex
	var failed int32
	sem := make(chan struct{}, genConcurrency)
	var wg sync.WaitGroup
	for _, sec := range leaves {
		wg.Add(1)
		sem <- struct{}{}
		go func(sec *section) {
			defer wg.Done()
			defer func() { <-sem }()
			its, err := s.extractItems(ctx, sec.title, cleanBody(sec.body.String()))
			if err != nil {
				atomic.AddInt32(&failed, 1)
				slog.Warn("roadmap: section extraction failed", "section", sec.title, "err", err)
				return
			}
			mu.Lock()
			itemsBy[sec] = its
			mu.Unlock()
		}(sec)
	}
	wg.Wait()
	if failed > 0 {
		slog.Warn("roadmap: some sections degraded to a single leaf", "failed", failed, "of", len(leaves))
	}

	total := 0
	add := func() bool { total++; return total <= maxItems }

	if noHeadings {
		var nodes []*treeNode
		for _, t := range dedupeStr(itemsBy[root]) {
			if !add() {
				break
			}
			nodes = append(nodes, &treeNode{Title: t})
		}
		return nodes, total, nil
	}

	var build func(sec *section) *treeNode
	build = func(sec *section) *treeNode {
		if len(sec.children) > 0 {
			n := &treeNode{Title: sec.title}
			add()
			for _, c := range sec.children {
				n.Children = append(n.Children, build(c))
			}
			return n
		}
		its := dedupeStr(itemsBy[sec])
		if len(its) > 0 {
			n := &treeNode{Title: sec.title}
			add()
			for _, t := range its {
				if !add() {
					break
				}
				n.Children = append(n.Children, &treeNode{Title: t})
			}
			return n
		}
		add()
		return &treeNode{Title: sec.title} // heading itself is the checkable leaf
	}
	var roots []*treeNode
	for _, c := range root.children {
		roots = append(roots, build(c))
	}
	return roots, total, nil
}

// persistTree writes the tree, deriving kind from structure. sort is a global
// counter so a pre-order walk restores the exact display order.
func (s *Service) persistTree(ctx context.Context, device, id uuid.UUID, roots []*treeNode, total int) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	hlc := s.feed.Clock.Now()
	sort := 0
	var insert func(nodes []*treeNode, parent *uuid.UUID, depth int) error
	insert = func(nodes []*treeNode, parent *uuid.UUID, depth int) error {
		for _, n := range nodes {
			kind := "leaf"
			if len(n.Children) > 0 {
				kind = "topic"
			}
			var newID uuid.UUID
			if err := tx.QueryRow(ctx, `
				INSERT INTO roadmap_items (roadmap_id, parent_id, title, depth, sort, kind, hlc)
				VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
				id, parent, trimTitle(n.Title), depth, sort, kind, hlc).Scan(&newID); err != nil {
				return err
			}
			sort++
			if len(n.Children) > 0 {
				if err := insert(n.Children, &newID, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := insert(roots, nil, 0); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE roadmaps SET status='ready', item_count=$2, updated_at=now() WHERE id=$1`,
		id, total); err != nil {
		return err
	}
	if _, err := s.feed.Append(ctx, tx, "roadmap", id, "upsert", device); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func dedupeStr(in []string) []string {
	seen := map[string]bool{}
	out := in[:0:0]
	for _, s := range in {
		k := norm(s)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, s)
	}
	return out
}

func norm(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = regexp.MustCompile(`[^a-z0-9 ]+`).ReplaceAllString(s, "")
	return strings.Join(strings.Fields(s), " ")
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

const itemSysPrompt = `You are given ONE section of a course's study material:
its heading and its text. List the concrete, checkable things a student should
revise for this section -- specific concepts, definitions, theorems, formulas,
derivations, algorithms, methods or problem types.

- Be exhaustive for this section, but use only what the text supports.
- Each item is short, specific and checkable ("Derive the normal equations"),
  not vague ("understand regression").
- Do not repeat the section heading as an item.

Respond ONLY with JSON: {"items":[{"title":"..."}]}`

var itemSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"items": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type":                 "object",
				"properties":           map[string]any{"title": map[string]any{"type": "string"}},
				"required":             []string{"title"},
				"additionalProperties": false,
			},
		},
	},
	"required":             []string{"items"},
	"additionalProperties": false,
}

// extractItems asks the model for the checklist items of one section, retrying
// transient failures and relaxing json_schema to json_object on the last try.
func (s *Service) extractItems(ctx context.Context, heading, body string) ([]string, error) {
	user := "SECTION: " + heading + "\n\nTEXT:\n" + body
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * 2 * time.Second):
			}
		}
		content, err := s.chat(ctx, itemSysPrompt, user, attempt < 2)
		if err != nil {
			lastErr = err
			continue
		}
		var parsed struct {
			Items []struct {
				Title string `json:"title"`
			} `json:"items"`
		}
		if err := json.Unmarshal([]byte(content), &parsed); err != nil {
			lastErr = fmt.Errorf("unparseable items: %w", err)
			continue
		}
		var out []string
		for _, it := range parsed.Items {
			if t := strings.TrimSpace(it.Title); t != "" {
				out = append(out, t)
			}
		}
		return out, nil
	}
	return nil, lastErr
}

// chat is one OpenRouter chat call. It surfaces the real status/body (including
// a 200-with-error envelope) instead of an opaque message.
func (s *Service) chat(ctx context.Context, system, user string, strict bool) (string, error) {
	var rf json.RawMessage
	if strict {
		b, _ := json.Marshal(map[string]any{
			"type":        "json_schema",
			"json_schema": map[string]any{"name": "items", "strict": true, "schema": itemSchema},
		})
		rf = b
	} else {
		rf = json.RawMessage(`{"type":"json_object"}`)
	}
	payload, _ := json.Marshal(map[string]any{
		"model":           s.model,
		"messages":        []chatMsg{{Role: "system", Content: system}, {Role: "user", Content: user}},
		"temperature":     0,
		"response_format": rf,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("HTTP-Referer", "https://badal.nandishwarsingh.com")
	req.Header.Set("X-Title", "badal")

	resp, err := s.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("openrouter status %d: %s", resp.StatusCode, trunc(string(raw), 300))
	}
	var env struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return "", fmt.Errorf("openrouter 200 unparseable envelope: %s", trunc(string(raw), 300))
	}
	if env.Error != nil {
		return "", fmt.Errorf("openrouter error: %s", env.Error.Message)
	}
	if len(env.Choices) == 0 {
		return "", fmt.Errorf("openrouter 200 with no choices: %s", trunc(string(raw), 300))
	}
	content := strings.TrimSpace(env.Choices[0].Message.Content)
	if content == "" {
		return "", fmt.Errorf("openrouter 200 empty content (finish=%s)", env.Choices[0].FinishReason)
	}
	return content, nil
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
