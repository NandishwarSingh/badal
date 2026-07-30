// Package vision reads text out of images.
//
// Routing: the Mac worker first, because it is free and the image never
// leaves the network. If no worker claims the job in time -- lid shut, away
// from home, asleep -- it falls back to a cheap cloud vision model rather
// than leaving a poster unread until the laptop happens to wake.
//
// The fallback model was chosen by testing, not by price alone: cheaper
// options silently dropped the last line of a test poster, which is exactly
// where contact details and registration deadlines live.
package vision

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const ocrPrompt = `Transcribe ALL text visible in this image, exactly as written.
Include every date, time, venue, deadline, price and contact detail.
Preserve line breaks. Output plain text only -- no commentary, no markdown.
If the image contains no readable text, output nothing.`

type Service struct {
	apiKey   string
	model    string
	http     *http.Client
	endpoint string
}

func New(apiKey, model string) *Service {
	if model == "" {
		// amazon/nova-lite-v1: $0.06/$0.24 per Mtok with no per-image
		// surcharge, and the cheapest model tested that transcribed a poster
		// completely including its final line.
		model = "amazon/nova-lite-v1"
	}
	return &Service{
		apiKey: apiKey, model: model,
		http:     &http.Client{Timeout: 120 * time.Second},
		endpoint: "https://openrouter.ai/api/v1/chat/completions",
	}
}

func (s *Service) Enabled() bool { return s.apiKey != "" }
func (s *Service) Model() string { return s.model }

// OCR returns the text in one image. mime must be the real content type --
// a data URL with the wrong type is rejected by some providers.
func (s *Service) OCR(ctx context.Context, img []byte, mime string) (string, error) {
	if !s.Enabled() {
		return "", fmt.Errorf("no api key")
	}
	if mime == "" {
		mime = "image/png"
	}
	url := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(img)

	payload, _ := json.Marshal(map[string]any{
		"model":       s.model,
		"temperature": 0,
		"max_tokens":  1200,
		"messages": []any{map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{"type": "text", "text": ocrPrompt},
				map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}},
			},
		}},
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
		return "", fmt.Errorf("vision %d: %s", resp.StatusCode, short(string(raw), 200))
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
		return "", err
	}
	if env.Error != nil {
		return "", fmt.Errorf("vision: %s", env.Error.Message)
	}
	if len(env.Choices) == 0 {
		return "", fmt.Errorf("no choices returned")
	}
	slog.Debug("ocr tokens", "in", env.Usage.PromptTokens, "out", env.Usage.CompletionTokens)
	return strings.TrimSpace(env.Choices[0].Message.Content), nil
}

func short(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
