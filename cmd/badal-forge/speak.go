package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// The speech side of the worker.
//
// Separate from the job queue on purpose. A job is claimed by polling, runs to
// completion and returns one result -- fine for OCR, useless for speech, where
// the first hundred milliseconds of audio matter more than the last second and
// an interruption has to land immediately. So this holds a socket open instead.
//
// The Python process holding Kokoro stays resident for the same reason: the
// model costs seconds to load and a sentence costs a fraction of one.

const (
	speakRetryMin = 2 * time.Second
	speakRetryMax = 30 * time.Second
)

type speaker struct {
	cmd  *exec.Cmd
	in   io.WriteCloser
	out  *bufio.Reader
	venv string
	tts  string
}

func ttsPaths() (venvPython, script string) {
	home, _ := os.UserHomeDir()
	venvPython = os.Getenv("BADAL_TTS_PYTHON")
	if venvPython == "" {
		venvPython = filepath.Join(home, ".badal", "tts-venv", "bin", "python")
	}
	script = os.Getenv("BADAL_TTS_SCRIPT")
	if script == "" {
		script = filepath.Join(home, ".local", "bin", "badal-tts.py")
	}
	return
}

func startSpeaker(ctx context.Context) (*speaker, error) {
	py, script := ttsPaths()
	if _, err := os.Stat(py); err != nil {
		return nil, err
	}
	if _, err := os.Stat(script); err != nil {
		return nil, err
	}
	// -u so frames are not held in Python's own buffer.
	cmd := exec.CommandContext(ctx, py, "-u", script)
	cmd.Stderr = os.Stderr // library chatter goes to the log, never to stdout
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &speaker{cmd: cmd, in: in, out: bufio.NewReaderSize(out, 1<<16)}, nil
}

func (s *speaker) say(text, voice string) error {
	b, _ := json.Marshal(map[string]any{"text": text, "voice": voice})
	_, err := s.in.Write(append(b, '\n'))
	return err
}

func (s *speaker) cancel() error {
	_, err := s.in.Write([]byte(`{"cancel":true}` + "\n"))
	return err
}

// readFrame pulls one [4-byte length][1-byte type][payload] frame.
func (s *speaker) readFrame() (byte, []byte, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(s.out, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(hdr[0:4])
	if n > 8<<20 {
		return 0, nil, io.ErrUnexpectedEOF
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(s.out, buf); err != nil {
		return 0, nil, err
	}
	return hdr[4], buf, nil
}

// runSpeech keeps the socket up for as long as the worker runs.
func runSpeech(ctx context.Context, apiURL, token string) {
	backoff := speakRetryMin
	for {
		if ctx.Err() != nil {
			return
		}
		if err := speechSession(ctx, apiURL, token); err != nil && ctx.Err() == nil {
			slog.Warn("voice: session ended", "err", err, "retry_in", backoff)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		// Backing off matters: the VPS restarts during a deploy, and a tight
		// reconnect loop from the worker turns that into a small flood.
		if backoff *= 2; backoff > speakRetryMax {
			backoff = speakRetryMax
		}
	}
}

func speechSession(ctx context.Context, apiURL, token string) error {
	sp, err := startSpeaker(ctx)
	if err != nil {
		// No model or no venv is not an error worth spinning on; the server
		// tells the phone to use its own voice instead.
		return err
	}
	defer func() {
		_ = sp.in.Close()
		_ = sp.cmd.Process.Kill()
		_, _ = sp.cmd.Process.Wait()
	}()

	wsURL := strings.Replace(apiURL, "http", "ws", 1) + "/v1/voice/worker"
	ws, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader:      http.Header{"Authorization": {"Bearer " + token}},
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return err
	}
	defer ws.CloseNow()
	ws.SetReadLimit(1 << 20)
	slog.Info("voice: connected, speech available")

	sessCtx, stop := context.WithCancel(ctx)
	defer stop()

	// Frames flow out as fast as Kokoro makes them, independently of whatever
	// is arriving, so a long sentence never blocks a cancel.
	errc := make(chan error, 2)
	go func() {
		for {
			kind, payload, err := sp.readFrame()
			if err != nil {
				errc <- err
				return
			}
			msg := append([]byte{kind}, payload...)
			wctx, cancel := context.WithTimeout(sessCtx, 10*time.Second)
			err = ws.Write(wctx, websocket.MessageBinary, msg)
			cancel()
			if err != nil {
				errc <- err
				return
			}
		}
	}()

	go func() {
		for {
			_, data, err := ws.Read(sessCtx)
			if err != nil {
				errc <- err
				return
			}
			var m struct {
				Say    string `json:"say"`
				Voice  string `json:"voice"`
				Cancel bool   `json:"cancel"`
			}
			if json.Unmarshal(data, &m) != nil {
				continue
			}
			if m.Cancel {
				_ = sp.cancel()
				continue
			}
			if strings.TrimSpace(m.Say) != "" {
				if err := sp.say(m.Say, m.Voice); err != nil {
					errc <- err
					return
				}
			}
		}
	}()

	select {
	case <-sessCtx.Done():
		return nil
	case err := <-errc:
		return err
	}
}
