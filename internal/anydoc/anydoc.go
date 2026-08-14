// Package anydoc turns an uploaded document into clean GitHub-Flavored Markdown
// by shelling out to Firecrawl's anydoc converter.
//
// anydoc is a fast, offline, pure-Rust tool (PDF and Office formats). We invoke
// it rather than reimplement extraction: it preserves headings, tables and
// lists, which is exactly the structure a roadmap is built from. It extracts
// only embedded text -- a scanned, image-only PDF yields little or nothing, and
// the caller treats an empty result as "needs OCR" rather than a silent drop.
package anydoc

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type Converter struct {
	argv    []string      // e.g. ["npx","-y","@firecrawl/anydoc"] or ["/usr/local/bin/anydoc"]
	timeout time.Duration
}

// New builds a converter from a command string. Empty falls back to the npx
// package, which works anywhere Node is present; production sets
// BADAL_ANYDOC_CMD to a prebuilt binary path.
func New(cmd string) *Converter {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		cmd = "npx -y @firecrawl/anydoc"
	}
	return &Converter{argv: strings.Fields(cmd), timeout: 60 * time.Second}
}

func (c *Converter) Enabled() bool { return len(c.argv) > 0 }

// Convert runs anydoc on a file already on disk and returns its Markdown.
func (c *Converter) Convert(ctx context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	args := append(append([]string{}, c.argv[1:]...), path)
	cmd := exec.CommandContext(ctx, c.argv[0], args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("anydoc %s: %w: %s", filepath.Base(path), err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// ConvertBytes writes bytes to a temp file (anydoc detects format from content,
// but a matching extension keeps its heuristics happy) and converts them.
func (c *Converter) ConvertBytes(ctx context.Context, data []byte, ext string) (string, error) {
	if ext == "" {
		ext = ".pdf"
	}
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	f, err := os.CreateTemp("", "anydoc-*"+ext)
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return "", err
	}
	f.Close()
	return c.Convert(ctx, f.Name())
}
