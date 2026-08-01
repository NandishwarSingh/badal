package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

// Query expansion, and why one question is not enough.
//
// "Tell me opportunities to make money" retrieved five things containing the
// word "opportunity" and nothing about money. The internship paying ₹15,000 in
// commission never surfaced, because nothing in it says "money" -- it says
// commission, stipend, funded, paid. Embeddings narrow the gap but do not
// close it, and reranking cannot rescue a candidate set that never contained
// the answer.
//
// So the question is rewritten into several, each using vocabulary the corpus
// plausibly contains, and the results are fused. It is the same call that
// resolves "and who do I email about it" against the previous turn, because
// both are the same problem: what the person said is not what the index holds.

const maxQueries = 4

type Turn struct {
	Role    string `json:"role"` // user | assistant
	Content string `json:"content"`
}

type expansion struct {
	Standalone string   `json:"standalone"`
	Queries    []string `json:"queries"`
}

const rewritePrompt = `You prepare search queries for a personal archive of a
university student's email, notes, calendar events and tasks.

Given the conversation and the newest question, return:

- "standalone": the newest question rewritten so it makes sense alone.
  Resolve every pronoun and reference against the conversation. If it already
  stands alone, repeat it unchanged.
- "queries": 2 to 3 ADDITIONAL search phrasings that use the concrete words
  documents would actually contain, not the words the person used.

Rules for the extra queries:
- Trade abstract wording for concrete. "Make money" should become things like
  "paid internship stipend", "commission per project", "scholarship award
  amount", "cash prize winner".
- Trade a category for its members. "Free food" should become "mess menu",
  "refreshments provided", "lunch will be served".
- Keep each under about eight words. No punctuation, no quotes.
- Do not restate the standalone query. Do not invent facts or names.`

var expandSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"standalone": map[string]any{"type": "string"},
		"queries": map[string]any{
			"type":     "array",
			"maxItems": 3,
			"items":    map[string]any{"type": "string"},
		},
	},
	"required":             []string{"standalone", "queries"},
	"additionalProperties": false,
}

// expand returns the queries to retrieve for, standalone first.
//
// It never fails the request: if the model is unreachable or answers badly the
// original question is used on its own, which is exactly the behaviour before
// this existed.
func (s *Service) expand(ctx context.Context, question string, history []Turn) []string {
	only := []string{question}

	var b strings.Builder
	if len(history) > 0 {
		b.WriteString("Conversation so far:\n")
		for _, t := range history {
			fmt.Fprintf(&b, "%s: %s\n", t.Role, trim(squeeze(t.Content), 400))
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "Newest question: %s", question)

	rf, _ := json.Marshal(map[string]any{
		"type":        "json_schema",
		"json_schema": map[string]any{"name": "expansion", "strict": true, "schema": expandSchema},
	})
	payload, _ := json.Marshal(map[string]any{
		"model": s.model,
		"messages": []chatMsg{
			{Role: "system", Content: rewritePrompt},
			{Role: "user", Content: b.String()},
		},
		"temperature":     0,
		"max_tokens":      220,
		"response_format": json.RawMessage(rf),
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(payload))
	if err != nil {
		return only
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("HTTP-Referer", "https://badal.nandishwarsingh.com")
	req.Header.Set("X-Title", "badal")

	resp, err := s.http.Do(req)
	if err != nil {
		slog.Warn("expand", "err", err)
		return only
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return only
	}
	var env struct {
		Choices []struct {
			Message struct{ Content string } `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &env); err != nil || len(env.Choices) == 0 {
		return only
	}
	var ex expansion
	if err := json.Unmarshal([]byte(env.Choices[0].Message.Content), &ex); err != nil {
		return only
	}

	out := []string{}
	seen := map[string]bool{}
	add := func(q string) {
		q = strings.TrimSpace(q)
		k := strings.ToLower(q)
		if q == "" || seen[k] || len(out) >= maxQueries {
			return
		}
		seen[k] = true
		out = append(out, q)
	}
	// Standalone leads: it is the one query guaranteed to be on-topic, and
	// fusion weights the first-ranked results of each arm equally.
	add(ex.Standalone)
	add(question)
	for _, q := range ex.Queries {
		add(q)
	}
	if len(out) == 0 {
		return only
	}
	return out
}
