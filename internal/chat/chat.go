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
	"strings"
	"time"

	"github.com/NandishwarSingh/badal/internal/embed"
)

const (
	// How many sources reach the prompt. Beyond about six the model starts
	// averaging across them instead of answering from the best one, and the
	// citation numbers stop meaning much.
	topK = 6

	// Per source. Enough for a section, short of a whole forwarded thread.
	maxSourceChars = 1400
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
- If the sources do not answer the question, say so plainly in one sentence
  and stop. Do not guess, and do not pad the answer with what they do contain.
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
	Sources []Source `json:"sources,omitempty"`
	Delta   string   `json:"delta,omitempty"`
	Done    bool     `json:"done,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// Ask retrieves, prompts, and streams. Each event is handed to emit as it
// happens; emit is expected to flush.
func (s *Service) Ask(ctx context.Context, question string, subjects []string, emit func(Event) error) error {
	q := strings.TrimSpace(question)
	if q == "" {
		return emit(Event{Error: "ask something", Done: true})
	}
	if !s.Enabled() {
		return emit(Event{Error: "chat is not configured", Done: true})
	}

	hits, err := s.emb.Retrieve(ctx, q, topK, nil, subjects, true)
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

	user := fmt.Sprintf("Sources:\n\n%s---\n\nToday is %s.\nQuestion: %s",
		ctxBuf.String(), time.Now().Format("Monday 2 January 2006"), q)

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
			if err := emit(Event{Delta: d}); err != nil {
				return err // client hung up
			}
		}
	}
	if err := sc.Err(); err != nil && ctx.Err() == nil {
		return emit(Event{Error: "stream interrupted", Done: true})
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
