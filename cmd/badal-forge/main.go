// Command badal-forge is the worker that runs on the Mac.
//
// It pulls leased jobs from badal-api, does the work locally, and reports
// back. Everything it does could be done in the cloud; the point is that it
// is free, and the images and audio never leave the network.
//
// It assumes it will be killed without warning -- lid shut, wifi gone, battery
// dead. Nothing is cleaned up on exit because nothing can be relied on to run
// on exit. The server's lease expiry is what recovers the work.
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

var (
	apiURL   = flag.String("api", envOr("BADAL_API", "https://badal.nandishwarsingh.com"), "badal-api base url")
	token    = flag.String("token", os.Getenv("BADAL_TOKEN"), "device token")
	owner    = flag.String("owner", hostname(), "worker identity")
	poll     = flag.Duration("poll", 5*time.Second, "how often to look for work")
	maxJobs  = flag.Int("concurrency", 2, "jobs in flight")
	maxTempC = flag.Float64("max-temp", 90, "stop claiming above this CPU temperature")
	ocrBin   = flag.String("ocr", "/usr/local/bin/badal-ocr", "path to the Vision OCR helper")
)

func main() {
	flag.Parse()
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	if *token == "" {
		slog.Error("BADAL_TOKEN is required")
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	kinds := []string{"ocr"}
	slog.Info("badal-forge starting", "api", *apiURL, "owner", *owner, "kinds", kinds)

	sem := make(chan struct{}, *maxJobs)
	var wg sync.WaitGroup

	t := time.NewTicker(*poll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("shutting down; in-flight leases will expire and requeue")
			wg.Wait()
			return
		case <-t.C:
		}

		// A fanless Mac under sustained load throttles. Stopping claiming is
		// better than accepting work we will finish slowly and hot.
		if c := cpuTemp(); c > 0 && c > *maxTempC {
			slog.Warn("too hot to claim", "tempC", c)
			continue
		}

		free := *maxJobs - len(sem)
		if free <= 0 {
			continue
		}
		claimed, err := claim(ctx, kinds, free)
		if err != nil {
			slog.Warn("claim", "err", err)
			continue
		}
		for _, j := range claimed {
			sem <- struct{}{}
			wg.Add(1)
			go func(j job) {
				defer func() { <-sem; wg.Done() }()
				run(ctx, j)
			}(j)
		}
	}
}

type job struct {
	ID      string          `json:"id"`
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

func run(ctx context.Context, j job) {
	// Hold the lease for as long as the work takes. Without this a slow OCR
	// gets reaped out from under itself and runs twice.
	hbCtx, stopHB := context.WithCancel(ctx)
	defer stopHB()
	go heartbeat(hbCtx, j.ID)

	// caffeinate for the duration: closing the lid mid-job must not kill work
	// that is already running.
	caf := exec.Command("caffeinate", "-dimsu", "-w", fmt.Sprint(os.Getpid()))
	_ = caf.Start()
	defer func() { _ = caf.Process.Kill() }()

	started := time.Now()
	var result any
	var err error

	switch j.Kind {
	case "ocr":
		result, err = doOCR(ctx, j.Payload)
	default:
		err = fmt.Errorf("unknown kind %q", j.Kind)
	}

	if err != nil {
		slog.Warn("job failed", "id", j.ID, "kind", j.Kind, "err", err)
		_ = post(ctx, "/v1/jobs/"+j.ID+"/fail", map[string]any{"owner": *owner, "error": err.Error()}, nil)
		return
	}
	if e := post(ctx, "/v1/jobs/"+j.ID+"/complete",
		map[string]any{"owner": *owner, "result": result}, nil); e != nil {
		slog.Warn("complete", "id", j.ID, "err", e)
		return
	}
	slog.Info("job done", "id", j.ID, "kind", j.Kind, "took", time.Since(started).Round(time.Millisecond))
}

type ocrPayload struct {
	Key   string `json:"key"`
	MIME  string `json:"mime"`
	Image string `json:"image"` // base64
}

func doOCR(ctx context.Context, raw json.RawMessage) (any, error) {
	var p ocrPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	img, err := base64.StdEncoding.DecodeString(p.Image)
	if err != nil {
		return nil, fmt.Errorf("bad image: %w", err)
	}
	ext := ".png"
	if strings.Contains(p.MIME, "jpeg") || strings.Contains(p.MIME, "jpg") {
		ext = ".jpg"
	}
	f, err := os.CreateTemp("", "badal-ocr-*"+ext)
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(img); err != nil {
		f.Close()
		return nil, err
	}
	f.Close()

	cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, *ocrBin, f.Name())
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s: %s", err, strings.TrimSpace(errb.String()))
	}
	return map[string]any{
		"text":   strings.TrimSpace(out.String()),
		"engine": "apple-vision",
	}, nil
}

func heartbeat(ctx context.Context, id string) {
	t := time.NewTicker(25 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := post(ctx, "/v1/jobs/"+id+"/heartbeat",
				map[string]any{"owner": *owner}, nil); err != nil {
				slog.Warn("heartbeat lost", "id", id, "err", err)
			}
		}
	}
}

func claim(ctx context.Context, kinds []string, n int) ([]job, error) {
	var out struct {
		Jobs []job `json:"jobs"`
	}
	err := post(ctx, "/v1/jobs/claim",
		map[string]any{"owner": *owner, "kinds": kinds, "n": n}, &out)
	return out.Jobs, err
}

var client = &http.Client{Timeout: 3 * time.Minute}

func post(ctx context.Context, path string, body any, into any) error {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, *apiURL+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+*token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s -> %d: %s", path, resp.StatusCode, trunc(string(raw), 160))
	}
	if into != nil {
		return json.Unmarshal(raw, into)
	}
	return nil
}

// cpuTemp reads the SoC die temperature. Returns 0 when unavailable, which is
// treated as "no thermal information" rather than "cold".
func cpuTemp() float64 {
	out, err := exec.Command("sh", "-c",
		`powermetrics -n 1 -i 200 --samplers smc 2>/dev/null | awk '/die temperature/{print $4; exit}'`).Output()
	if err != nil {
		return 0
	}
	var c float64
	_, _ = fmt.Sscanf(strings.TrimSpace(string(out)), "%f", &c)
	return c
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "worker"
	}
	return strings.TrimSuffix(filepath.Base(h), ".local")
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
