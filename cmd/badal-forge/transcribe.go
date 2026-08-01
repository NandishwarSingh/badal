package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Speech to text, on this machine.
//
// whisper.cpp rather than a cloud API: the audio is you talking about your own
// mail, and it never has to leave the laptop. The model is a one-off 466MB
// download and everything after that is free and offline.
//
// The recording arrives as whatever the browser produced -- webm/opus on
// Android, mp4/aac on iOS -- and whisper wants 16kHz mono PCM, so ffmpeg sits
// in the middle. That conversion is also where a phone's 48kHz stereo gets
// halved twice over, which matters more than it sounds on a fanless Air.

type transcribePayload struct {
	MIME  string `json:"mime"`
	Audio string `json:"audio"` // base64
}

type transcribeResult struct {
	Text string `json:"text"`
	Secs int    `json:"secs"`
}

// launchd hands an agent a minimal PATH that excludes /opt/homebrew/bin, so
// a bare exec.Command("ffmpeg") fails with "executable file not found" on a
// machine where ffmpeg is plainly installed. The OCR helper never hit this
// because it is passed in as an absolute path.
func tool(name string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	for _, dir := range []string{"/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/opt/local/bin"} {
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s not found; install it or add it to the agent's PATH", name)
}

func modelPath() string {
	if v := os.Getenv("BADAL_WHISPER_MODEL"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".badal", "models", "ggml-small.en.bin")
}

func doTranscribe(ctx context.Context, raw json.RawMessage) (any, error) {
	var p transcribePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	audio, err := base64.StdEncoding.DecodeString(p.Audio)
	if err != nil {
		return nil, fmt.Errorf("bad audio: %w", err)
	}
	model := modelPath()
	if _, err := os.Stat(model); err != nil {
		return nil, fmt.Errorf("whisper model missing at %s", model)
	}

	dir, err := os.MkdirTemp("", "badal-asr-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	src := filepath.Join(dir, "in"+audioExt(p.MIME))
	if err := os.WriteFile(src, audio, 0o600); err != nil {
		return nil, err
	}
	wav := filepath.Join(dir, "in.wav")

	// 16kHz mono is what the model was trained on; anything else is resampled
	// internally anyway, and doing it here keeps the file small.
	ff, err := tool("ffmpeg")
	if err != nil {
		return nil, err
	}
	conv := exec.CommandContext(ctx, ff, "-nostdin", "-loglevel", "error",
		"-i", src, "-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le", wav)
	if out, err := conv.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("ffmpeg: %v: %s", err, trimOut(out))
	}

	started := time.Now()
	// -nt strips timestamps, -np the progress bar: this is a question being
	// dictated, not a transcript that needs a timeline.
	wc, err := tool("whisper-cli")
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, wc,
		"-m", model, "-f", wav, "-l", "en", "-nt", "-np", "-t", "4")
	// Stdout only. whisper.cpp writes a page of Metal and backend chatter to
	// stderr on every run, and CombinedOutput handed all of it back as the
	// transcript.
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("whisper: %v: %s", err, trimOut(errBuf.Bytes()))
	}

	text := strings.TrimSpace(string(out))
	// whisper emits bracketed markers for non-speech; a question that was
	// entirely a cough should come back empty rather than as "[BLANK_AUDIO]".
	for _, marker := range []string{"[BLANK_AUDIO]", "(silence)", "[silence]", "[ Silence ]"} {
		text = strings.ReplaceAll(text, marker, "")
	}
	text = strings.Join(strings.Fields(text), " ")

	return transcribeResult{Text: text, Secs: int(time.Since(started).Seconds())}, nil
}

func audioExt(mime string) string {
	switch {
	case strings.Contains(mime, "webm"):
		return ".webm"
	case strings.Contains(mime, "ogg"):
		return ".ogg"
	case strings.Contains(mime, "wav"):
		return ".wav"
	case strings.Contains(mime, "mp4"), strings.Contains(mime, "m4a"), strings.Contains(mime, "aac"):
		return ".m4a"
	default:
		return ".bin"
	}
}

func trimOut(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		return s[:300]
	}
	return s
}
