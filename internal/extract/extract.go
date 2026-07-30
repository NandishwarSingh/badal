// Package extract turns email text into proposed calendar events.
//
// Three rules shape everything here, and each exists because of a specific way
// this feature fails otherwise:
//
//  1. Every event must quote the span that justifies it, and the server checks
//     that span really occurs in the message. Models invent plausible dates;
//     this rejects them mechanically instead of hoping.
//  2. Relative dates resolve against the message's received time, never now().
//     "Friday 3pm" means something different when re-extracted in November.
//  3. Nothing is written as confirmed unless the user turned that on. An
//     unreviewed model write is what makes a calendar untrustworthy, and a
//     calendar you do not trust is worse than no calendar.
package extract

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
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Version is baked into every extracted row. Bump it when the prompt or schema
// changes; that is what makes re-extraction produce a new version to diff
// rather than duplicate events.
const Version = 1

const maxBodyChars = 12000

type Service struct {
	pool     *pgxpool.Pool
	apiKey   string
	model    string
	loc      *time.Location
	http     *http.Client
	endpoint string
}

func New(pool *pgxpool.Pool, apiKey, model, tz string) *Service {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.UTC
	}
	if model == "" {
		model = "deepseek/deepseek-v4-flash"
	}
	return &Service{
		pool: pool, apiKey: apiKey, model: model, loc: loc,
		http:     &http.Client{Timeout: 90 * time.Second},
		endpoint: "https://openrouter.ai/api/v1/chat/completions",
	}
}

func (s *Service) Enabled() bool { return s.apiKey != "" }

// ------------------------------------------------------------------ model

type Extracted struct {
	Events []Event `json:"events"`
	Junk   bool    `json:"is_junk"`
}

type Event struct {
	Title      string  `json:"title"`
	StartsAt   string  `json:"starts_at"`
	EndsAt     string  `json:"ends_at"`
	AllDay     bool    `json:"all_day"`
	Location   string  `json:"location"`
	Kind       string  `json:"kind"`
	Confidence float64 `json:"confidence"`
	Evidence   string  `json:"evidence"`
}

const systemPrompt = `You extract calendar events from university email.

Return ONLY events with a concrete date. Skip anything vague ("soon", "later
this semester") and skip pure announcements with no date.

Rules:
- starts_at and ends_at MUST be absolute ISO-8601 with offset, e.g.
  2026-08-14T15:00:00+05:30. Never return a relative expression.
- Resolve relative wording ("tomorrow", "this Friday", "next week") against
  the RECEIVED time given below, not against today.
- If only a date is given with no time, set all_day true and use T00:00:00.
- evidence MUST be a short verbatim substring copied exactly from the email
  body or subject that states the date. Do not paraphrase it. If you cannot
  quote it, do not emit the event.
- kind is one of: exam, deadline, class, event, meeting, task.
- confidence 0..1 reflects how certain the date and title are.
- Registration/RSVP cut-offs are kind=deadline. Talks, fests and workshops are
  kind=event.
- is_junk true for newsletters, promotions and security alerts with no dates.

Prefer emitting nothing over guessing.`

var schema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"is_junk": map[string]any{"type": "boolean"},
		"events": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"title":      map[string]any{"type": "string"},
					"starts_at":  map[string]any{"type": "string"},
					"ends_at":    map[string]any{"type": "string"},
					"all_day":    map[string]any{"type": "boolean"},
					"location":   map[string]any{"type": "string"},
					"kind":       map[string]any{"type": "string", "enum": []string{"exam", "deadline", "class", "event", "meeting", "task"}},
					"confidence": map[string]any{"type": "number"},
					"evidence":   map[string]any{"type": "string"},
				},
				"required":             []string{"title", "starts_at", "all_day", "kind", "confidence", "evidence"},
				"additionalProperties": false,
			},
		},
	},
	"required":             []string{"events", "is_junk"},
	"additionalProperties": false,
}

type Message struct {
	ID         uuid.UUID
	Subject    string
	FromName   string
	FromAddr   string
	ReceivedAt time.Time
	Body       string
}

// Result is what one message produced.
type Result struct {
	MessageID uuid.UUID `json:"message_id"`
	Subject   string    `json:"subject"`
	Found     int       `json:"found"`
	Kept      int       `json:"kept"`
	Rejected  []string  `json:"rejected,omitempty"`
	Junk      bool      `json:"junk"`
	Error     string    `json:"error,omitempty"`
}

func (s *Service) Extract(ctx context.Context, m Message, autoConfirm bool, minConf float64) (Result, error) {
	res := Result{MessageID: m.ID, Subject: m.Subject}

	body := m.Body
	if len(body) > maxBodyChars {
		body = body[:maxBodyChars]
	}
	recv := m.ReceivedAt.In(s.loc)
	user := fmt.Sprintf(
		"RECEIVED: %s (%s)\nUSER TIMEZONE: %s\nFROM: %s <%s>\nSUBJECT: %s\n\nBODY:\n%s",
		recv.Format(time.RFC3339), recv.Format("Monday 2 January 2006"),
		s.loc.String(), m.FromName, m.FromAddr, m.Subject, body)

	out, err := s.call(ctx, user)
	if err != nil {
		res.Error = err.Error()
		return res, err
	}
	res.Junk = out.Junk
	res.Found = len(out.Events)

	// The haystack the evidence must be found in.
	hay := normalise(m.Subject + "\n" + m.Body)

	for _, e := range out.Events {
		if strings.TrimSpace(e.Title) == "" {
			res.Rejected = append(res.Rejected, "empty title")
			continue
		}
		// Rule 1: the quote must actually exist in the source.
		ev := normalise(e.Evidence)
		if len(ev) < 4 || !strings.Contains(hay, ev) {
			res.Rejected = append(res.Rejected,
				fmt.Sprintf("%s: evidence not found in source", trim(e.Title, 40)))
			continue
		}
		start, err := time.Parse(time.RFC3339, e.StartsAt)
		if err != nil {
			res.Rejected = append(res.Rejected,
				fmt.Sprintf("%s: unparseable start %q", trim(e.Title, 40), e.StartsAt))
			continue
		}
		// A date years from the message is almost always a misparse.
		if start.Before(recv.AddDate(0, 0, -2)) || start.After(recv.AddDate(2, 0, 0)) {
			res.Rejected = append(res.Rejected,
				fmt.Sprintf("%s: date %s implausible vs received", trim(e.Title, 40), start.Format("2006-01-02")))
			continue
		}
		var end *time.Time
		if e.EndsAt != "" {
			if t, err := time.Parse(time.RFC3339, e.EndsAt); err == nil && t.After(start) {
				end = &t
			}
		}

		state := "proposed"
		if autoConfirm && e.Confidence >= minConf {
			state = "confirmed"
		}
		if err := s.upsert(ctx, m.ID, e, start, end, state); err != nil {
			res.Rejected = append(res.Rejected, fmt.Sprintf("%s: %v", trim(e.Title, 40), err))
			continue
		}
		res.Kept++
	}
	return res, nil
}

func (s *Service) upsert(ctx context.Context, msgID uuid.UUID, e Event,
	start time.Time, end *time.Time, state string) error {
	kind := e.Kind
	switch kind {
	case "exam", "deadline", "class", "event", "meeting", "task":
	default:
		kind = "event"
	}
	// Cross-message dedupe first. University events are announced repeatedly --
	// the same fest lands from three departments -- and the per-message
	// ON CONFLICT below cannot see those. Match on title and start time, and
	// leave the existing row's state alone so a decision the user already made
	// is never silently reopened.
	// Titles vary between announcements of the same thing -- "UMANG 2026" and
	// "UMANG 2026 - a spectacular cultural celebration" are one event. Treat a
	// prefix match at the same time as the same event, which catches the
	// common "short name" vs "full blurb" pair without merging genuinely
	// different events that happen to share an hour.
	title := strings.TrimSpace(e.Title)
	var dupID uuid.UUID
	err := s.pool.QueryRow(ctx,
		`SELECT id FROM events
		  WHERE deleted_at IS NULL
		    AND abs(extract(epoch from (starts_at - $2))) < 1800
		    AND (lower(title) = lower($1)
		      OR lower(title) LIKE lower($1) || '%'
		      OR lower($1) LIKE lower(title) || '%')
		  ORDER BY length(title) LIMIT 1`, title, start).Scan(&dupID)
	if err == nil {
		// Keep whichever extraction was more confident, but never downgrade
		// evidence we already have.
		// Keep the shorter title: "UMANG 2026" is a better calendar entry than
		// the full marketing sentence.
		_, uerr := s.pool.Exec(ctx,
			`UPDATE events
			    SET title      = CASE WHEN length($5) < length(title) THEN $5 ELSE title END,
			        confidence = greatest(confidence, $2),
			        location   = coalesce(nullif($3,''), location),
			        ends_at    = coalesce(ends_at, $4),
			        updated_at = now()
			  WHERE id = $1`, dupID, e.Confidence, e.Location, end, title)
		return uerr
	}

	// ON CONFLICT on the dedupe index: re-extracting the same message updates
	// the row instead of adding a second copy of the same deadline. A row the
	// user already acted on keeps its state.
	_, err = s.pool.Exec(ctx,
		`INSERT INTO events (title, starts_at, ends_at, all_day, location, kind,
		                     state, confidence, evidence, source_message, extraction_version)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		 ON CONFLICT (source_message, lower(title), starts_at)
		   WHERE source_message IS NOT NULL AND deleted_at IS NULL
		 DO UPDATE SET ends_at=EXCLUDED.ends_at, location=EXCLUDED.location,
		               kind=EXCLUDED.kind, confidence=EXCLUDED.confidence,
		               evidence=EXCLUDED.evidence,
		               extraction_version=EXCLUDED.extraction_version,
		               updated_at=now()`,
		strings.TrimSpace(e.Title), start, end, e.AllDay, e.Location, kind,
		state, e.Confidence, e.Evidence, msgID, Version)
	return err
}

// ------------------------------------------------------------- openrouter

type chatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (s *Service) call(ctx context.Context, user string) (Extracted, error) {
	var out Extracted

	rf, _ := json.Marshal(map[string]any{
		"type": "json_schema",
		"json_schema": map[string]any{
			"name": "extraction", "strict": true, "schema": schema,
		},
	})
	payload, _ := json.Marshal(map[string]any{
		"model": s.model,
		"messages": []chatMsg{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: user},
		},
		// Deterministic: the same message should extract the same way twice.
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
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return out, fmt.Errorf("decode: %w", err)
	}
	if env.Error != nil {
		return out, fmt.Errorf("openrouter: %s", env.Error.Message)
	}
	if len(env.Choices) == 0 {
		return out, fmt.Errorf("no choices returned")
	}
	content := strings.TrimSpace(env.Choices[0].Message.Content)
	// Some models wrap JSON in a fence despite structured output.
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")

	if err := json.Unmarshal([]byte(strings.TrimSpace(content)), &out); err != nil {
		return out, fmt.Errorf("model returned non-schema JSON: %w", err)
	}
	slog.Debug("extract tokens", "in", env.Usage.PromptTokens, "out", env.Usage.CompletionTokens)
	return out, nil
}

// ---------------------------------------------------------------- helpers

var wsRun = regexp.MustCompile(`\s+`)

// normalise makes the evidence check robust to the whitespace and quote
// mangling that happens between HTML mail and plain text, without becoming so
// loose that it stops catching invented quotes.
func normalise(s string) string {
	s = strings.ToLower(s)
	r := strings.NewReplacer(
		" ", " ", "‘", "'", "’", "'",
		"“", `"`, "”", `"`, "–", "-", "—", "-",
	)
	s = r.Replace(s)
	return strings.TrimSpace(wsRun.ReplaceAllString(s, " "))
}

func trim(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
