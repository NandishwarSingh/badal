// Package chat answers questions from the corpus, with sources.
//
// This is where the retrieval work cashes in. Everything the pipeline already
// does -- hybrid search, reciprocal rank fusion, cross-encoder reranking, and
// A3's section-level parents -- exists so that this prompt receives a handful
// of passages that are actually about the question, short enough to read and
// long enough to make sense on their own.
//
// Two rules shape the design, and both exist because of how this fails:
//
//  1. The model answers from the supplied passages or says it cannot. An
//     assistant over your own mail is worth nothing if you have to verify
//     every claim against the mail anyway.
//  2. Every answer carries its sources, numbered and tappable. A wrong answer
//     you can check in one tap is a nuisance; a wrong answer you cannot is a
//     reason to stop trusting the whole thing.
package chat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/NandishwarSingh/badal/internal/embed"
)

const (
	// How many sources reach the prompt. Beyond about eight the model starts
	// averaging across them instead of answering from the best one, and the
	// citation numbers stop meaning much.
	topK = 8

	// Per expanded query. Kept small because several arms are fused and only
	// topK survive; over-fetching each arm just slows the whole thing down.
	perQuery = 8

	// Same k as the search pipeline's fusion, and for the same reason: it
	// flattens the top ranks so a document several queries agree on beats one
	// that a single query loves.
	rrfK = 60.0

	// Per source. A3 caps a section at 2400, so trimming to 1400 was cutting
	// a third off every passage -- including, in one case, the line stating
	// the commission that made the email an answer at all. Matching the cap
	// means a cited section arrives whole.
	maxSourceChars = 2400
)

type Service struct {
	emb      *embed.Service
	apiKey   string
	model    string
	http     *http.Client
	endpoint string
}

func New(emb *embed.Service, apiKey, model string) *Service {
	if model == "" {
		model = "deepseek/deepseek-v4-flash"
	}
	return &Service{
		emb: emb, apiKey: apiKey, model: model,
		// No timeout: this is a streaming response and the context governs it.
		http:     &http.Client{},
		endpoint: "https://openrouter.ai/api/v1/chat/completions",
	}
}

func (s *Service) Enabled() bool { return s.apiKey != "" && s.emb.Enabled() }

// Source is one cited passage, as the client needs it to render a chip that
// opens the thing it came from.
type Source struct {
	N          int    `json:"n"`
	SourceType string `json:"source_type"`
	SourceID   string `json:"source_id"`
	Title      string `json:"title"`
	Snippet    string `json:"snippet"`
	Slug       string `json:"slug,omitempty"`
}

const systemPrompt = `You answer questions about the user's own notes, email, calendar and tasks.

You are given numbered sources. Follow these rules exactly:

- Answer ONLY from the sources. Never use outside knowledge about the world,
  the university, or anything else.
- Cite with [1], [2] inline, immediately after the claim each supports. Cite
  the specific source, not a list of everything provided.
- If the sources only partly cover the question, answer with what they DO
  support and add one short sentence naming what is missing. Partial is
  useful; refusing to answer when you hold half of it is not.
- Only say nothing is there when genuinely nothing among the sources is
  relevant. Never invent, never fill a gap from general knowledge.
- Broad questions ("what should I apply for", "ways to earn") want a short
  list of the concrete things in the sources, each cited. Do not decline
  because no single source is titled after the question.
- Prefer the shortest complete answer. Two sentences beat a paragraph.
- Dates and times: give them as they appear. Never compute a weekday or a
  relative date yourself.
- No preamble. Do not restate the question or say "based on the sources".`

type chatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Event is one thing the client is told, in order: sources first so the UI can
// render citation chips before the text that references them arrives, then
// deltas, then done.
type Event struct {
	// What it decided to look for. Sent before anything else so the wait has
	// something honest in it rather than a spinner.
	Queries []string `json:"queries,omitempty"`
	Sources []Source `json:"sources,omitempty"`
	Delta   string   `json:"delta,omitempty"`
	Done    bool     `json:"done,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// Ask retrieves, prompts, and streams. Each event is handed to emit as it
// happens; emit is expected to flush.
func (s *Service) Ask(ctx context.Context, question string, history []Turn, subjects []string, emit func(Event) error) error {
	q := strings.TrimSpace(question)
	if q == "" {
		return emit(Event{Error: "ask something", Done: true})
	}
	if !s.Enabled() {
		return emit(Event{Error: "chat is not configured", Done: true})
	}

	// Several phrasings, fused. One question is often not the question the
	// index can answer -- see rewrite.go.
	queries := s.expand(ctx, q, history)
	if err := emit(Event{Queries: queries}); err != nil {
		return err
	}

	hits, err := s.retrieveFused(ctx, queries, subjects)
	if err != nil {
		return emit(Event{Error: "could not search: " + err.Error(), Done: true})
	}
	if len(hits) == 0 {
		if err := emit(Event{Sources: []Source{}}); err != nil {
			return err
		}
		return emit(Event{Delta: "Nothing in your notes, mail or tasks covers that.", Done: true})
	}

	sources := make([]Source, 0, len(hits))
	var ctxBuf strings.Builder
	for i, h := range hits {
		// The section, not the matched fragment: a citation has to stand on
		// its own when the model quotes from it.
		body := h.Parent
		if strings.TrimSpace(body) == "" {
			body = h.Snippet
		}
		body = trim(squeeze(body), maxSourceChars)
		fmt.Fprintf(&ctxBuf, "[%d] %s (%s)\n%s\n\n", i+1, h.Title, kindWord(h.SourceType), body)
		sources = append(sources, Source{
			N:          i + 1,
			SourceType: h.SourceType,
			SourceID:   h.SourceID.String(),
			Title:      h.Title,
			Snippet:    trim(squeeze(h.Snippet), 180),
			Slug:       h.Slug,
		})
	}
	if err := emit(Event{Sources: sources}); err != nil {
		return err
	}

	var prior strings.Builder
	// Only the last exchange: enough for a follow-up to read naturally,
	// little enough that the model cannot start answering from the
	// conversation instead of from the sources.
	if n := len(history); n >= 2 {
		for _, t := range history[n-2:] {
			fmt.Fprintf(&prior, "%s: %s\n", t.Role, trim(squeeze(t.Content), 300))
		}
	}
	user := fmt.Sprintf("Sources:\n\n%s---\n\n%sToday is %s.\nQuestion: %s",
		ctxBuf.String(),
		func() string {
			if prior.Len() == 0 {
				return ""
			}
			return "Earlier in this conversation:\n" + prior.String() + "\n"
		}(),
		time.Now().Format("Monday 2 January 2006"), q)

	return s.stream(ctx, user, emit)
}

func (s *Service) stream(ctx context.Context, user string, emit func(Event) error) error {
	payload, _ := json.Marshal(map[string]any{
		"model": s.model,
		"messages": []chatMsg{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: user},
		},
		// Low but not zero: at zero the model repeats source wording verbatim
		// rather than answering in its own words.
		"temperature": 0.2,
		"max_tokens":  700,
		"stream":      true,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(payload))
	if err != nil {
		return emit(Event{Error: err.Error(), Done: true})
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("HTTP-Referer", "https://badal.nandishwarsingh.com")
	req.Header.Set("X-Title", "badal")

	resp, err := s.http.Do(req)
	if err != nil {
		return emit(Event{Error: "model unreachable", Done: true})
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 400))
		return emit(Event{Error: fmt.Sprintf("model %d: %s", resp.StatusCode, raw), Done: true})
	}

	// The upstream stream is itself SSE. Lines are re-emitted one delta at a
	// time rather than buffered, which is the entire point: the first words
	// should appear while the rest is still being generated.
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	// Upstream occasionally completes a stream having sent no content at all.
	// Silence is the one outcome the client cannot interpret -- it looks
	// identical to a bug here -- so it gets said out loud.
	wrote := false
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue // keep-alive comments and partial frames are not fatal
		}
		if chunk.Error != nil {
			return emit(Event{Error: chunk.Error.Message, Done: true})
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		if d := chunk.Choices[0].Delta.Content; d != "" {
			wrote = true
			if err := emit(Event{Delta: d}); err != nil {
				return err // client hung up
			}
		}
	}
	if err := sc.Err(); err != nil && ctx.Err() == nil {
		return emit(Event{Error: "stream interrupted", Done: true})
	}
	if !wrote {
		return emit(Event{
			Delta: "The model returned nothing that time. The sources below are what it was given — ask again.",
			Done:  true,
		})
	}
	return emit(Event{Done: true})
}

func kindWord(t string) string {
	switch t {
	case "message":
		return "email"
	case "event":
		return "calendar event"
	case "task":
		return "task"
	default:
		return "note"
	}
}

func squeeze(s string) string { return strings.Join(strings.Fields(s), " ") }

func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// retrieveFused runs every query and fuses the rankings.
//
// Reciprocal rank fusion rather than score averaging: the arms are separate
// searches whose scores are not comparable, but their ranks are. A source two
// phrasings both surface outranks one that a single phrasing loved.
func (s *Service) retrieveFused(ctx context.Context, queries []string, subjects []string) ([]embed.Hit, error) {
	if len(queries) == 1 {
		return s.emb.Retrieve(ctx, queries[0], topK, nil, subjects, true)
	}

	type result struct {
		hits []embed.Hit
		err  error
	}
	out := make([]result, len(queries))
	var wg sync.WaitGroup
	for i, q := range queries {
		wg.Add(1)
		go func(i int, q string) {
			defer wg.Done()
			h, err := s.emb.Retrieve(ctx, q, perQuery, nil, subjects, true)
			out[i] = result{h, err}
		}(i, q)
	}
	wg.Wait()

	type acc struct {
		hit   embed.Hit
		score float64
	}
	byID := map[string]*acc{}
	var firstErr error
	any := false
	for _, r := range out {
		if r.err != nil {
			firstErr = r.err
			continue
		}
		any = true
		for rank, h := range r.hits {
			k := h.SourceType + ":" + h.SourceID.String()
			a, ok := byID[k]
			if !ok {
				a = &acc{hit: h}
				byID[k] = a
			}
			a.score += 1 / (rrfK + float64(rank+1))
		}
	}
	// Every arm failing is a real error; one arm failing is not worth losing
	// an answer over.
	if !any && firstErr != nil {
		return nil, firstErr
	}

	fused := make([]*acc, 0, len(byID))
	for _, a := range byID {
		fused = append(fused, a)
	}
	sort.Slice(fused, func(i, j int) bool { return fused[i].score > fused[j].score })

	hits := make([]embed.Hit, 0, topK)
	for i, a := range fused {
		if i >= topK {
			break
		}
		hits = append(hits, a.hit)
	}
	return hits, nil
}
