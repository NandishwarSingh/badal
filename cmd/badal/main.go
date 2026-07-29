// Command badal is the command-line client.
//
// It exists early on purpose: every API endpoint gets exercised here before
// any UI is written, so the CLI doubles as the integration harness.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/NandishwarSingh/badal/internal/chunks"
)

type cliConfig struct {
	API   string `json:"api"`
	Token string `json:"token"`
}

func cfgPath() string {
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".config", "badal", "cli.json")
}

func loadCfg() (cliConfig, error) {
	var c cliConfig
	b, err := os.ReadFile(cfgPath())
	if err != nil {
		return c, fmt.Errorf("not configured — run: badal init <api-url> <token>")
	}
	return c, json.Unmarshal(b, &c)
}

func (c cliConfig) do(method, path string, body io.Reader, ctype string) (*http.Response, error) {
	req, err := http.NewRequest(method, strings.TrimRight(c.API, "/")+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	return (&http.Client{Timeout: 5 * time.Minute}).Do(req)
}

func (c cliConfig) json(method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	resp, err := c.do(method, path, body, "application/json")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s -> %s: %s", method, path, resp.Status, strings.TrimSpace(string(raw)))
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "init":
		err = cmdInit()
	case "push":
		err = cmdPush()
	case "pull":
		err = cmdPull()
	case "ls":
		err = cmdLs()
	case "rm":
		err = cmdRm()
	case "sync":
		err = cmdSync()
	case "devices":
		err = cmdDevices()
	case "health":
		err = cmdHealth()
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `badal — personal cloud CLI

  badal init <api-url> <token>     save credentials
  badal health                     server health
  badal push <local> [remote]      chunk, upload only what is missing, commit
  badal pull <remote> [local]      download and verify
  badal ls [prefix]                list files
  badal rm <remote>                soft-delete (tombstone propagates via sync)
  badal sync [since]               read the change feed
  badal devices                    list registered devices
`)
	os.Exit(2)
}

func cmdInit() error {
	if len(os.Args) < 4 {
		return fmt.Errorf("usage: badal init <api-url> <token>")
	}
	c := cliConfig{API: os.Args[2], Token: os.Args[3]}
	if err := os.MkdirAll(filepath.Dir(cfgPath()), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	if err := os.WriteFile(cfgPath(), b, 0o600); err != nil {
		return err
	}
	fmt.Println("saved", cfgPath())
	return nil
}

func cmdHealth() error {
	c, err := loadCfg()
	if err != nil {
		return err
	}
	resp, err := http.Get(strings.TrimRight(c.API, "/") + "/health")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	fmt.Printf("%s %s\n", resp.Status, strings.TrimSpace(string(b)))
	return nil
}

func cmdPush() error {
	if len(os.Args) < 3 {
		return fmt.Errorf("usage: badal push <local> [remote]")
	}
	c, err := loadCfg()
	if err != nil {
		return err
	}
	local := os.Args[2]
	remote := filepath.Base(local)
	if len(os.Args) > 3 {
		remote = os.Args[3]
	}
	f, err := os.Open(local)
	if err != nil {
		return err
	}
	defer f.Close()

	// Hash locally first so we can ask what the server is missing.
	type held struct {
		hash []byte
		data []byte
	}
	var order []string
	byHash := map[string]held{}
	whole, size, err := chunks.Split(f, chunks.DefaultOptions(), func(ch chunks.Chunk) error {
		h := chunks.Hex(ch.Hash)
		order = append(order, h)
		if _, seen := byHash[h]; !seen {
			byHash[h] = held{ch.Hash, ch.Data}
		}
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Printf("%s: %s in %d chunks (%d unique)\n", local, humanBytes(size), len(order), len(byHash))

	uniq := make([]string, 0, len(byHash))
	for h := range byHash {
		uniq = append(uniq, h)
	}
	var probe struct {
		Missing []string `json:"missing"`
		Have    int      `json:"have"`
	}
	if err := c.json("POST", "/v1/chunks/probe", map[string]any{"hashes": uniq}, &probe); err != nil {
		return err
	}
	fmt.Printf("server already has %d, uploading %d\n", probe.Have, len(probe.Missing))

	var sent int64
	for i, h := range probe.Missing {
		ch := byHash[h]
		resp, err := c.do("PUT", "/v1/chunks/"+h, bytes.NewReader(ch.data), "application/octet-stream")
		if err != nil {
			return err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			return fmt.Errorf("chunk %s: %s %s", h[:12], resp.Status, strings.TrimSpace(string(body)))
		}
		sent += int64(len(ch.data))
		fmt.Printf("\r  uploaded %d/%d (%s)", i+1, len(probe.Missing), humanBytes(sent))
	}
	if len(probe.Missing) > 0 {
		fmt.Println()
	}

	ct := mime.TypeByExtension(filepath.Ext(local))
	if ct == "" {
		ct = "application/octet-stream"
	}
	var out map[string]any
	if err := c.json("POST", "/v1/files", map[string]any{
		"path": remote, "hash": chunks.Hex(whole), "size": size,
		"chunks": order, "content_type": ct}, &out); err != nil {
		return err
	}
	fmt.Printf("committed %s  v%v  (%s transferred of %s)\n",
		out["path"], out["version"], humanBytes(sent), humanBytes(size))
	return nil
}

func cmdPull() error {
	if len(os.Args) < 3 {
		return fmt.Errorf("usage: badal pull <remote> [local]")
	}
	c, err := loadCfg()
	if err != nil {
		return err
	}
	remote := os.Args[2]
	local := filepath.Base(remote)
	if len(os.Args) > 3 {
		local = os.Args[3]
	}
	resp, err := c.do("GET", "/v1/blob/"+remote, nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusAccepted {
		b, _ := io.ReadAll(resp.Body)
		fmt.Println("archived —", strings.TrimSpace(string(b)))
		return nil
	}
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	out, err := os.Create(local)
	if err != nil {
		return err
	}
	defer out.Close()
	n, err := io.Copy(out, resp.Body)
	if err != nil {
		return err
	}
	fmt.Printf("wrote %s (%s)\n", local, humanBytes(n))
	return nil
}

func cmdLs() error {
	c, err := loadCfg()
	if err != nil {
		return err
	}
	prefix := ""
	if len(os.Args) > 2 {
		prefix = os.Args[2]
	}
	var out struct {
		Files []struct {
			Path    string `json:"path"`
			Size    int64  `json:"size"`
			Version int    `json:"version"`
			Hash    string `json:"hash"`
		} `json:"files"`
	}
	if err := c.json("GET", "/v1/files?prefix="+prefix, nil, &out); err != nil {
		return err
	}
	if len(out.Files) == 0 {
		fmt.Println("(no files)")
		return nil
	}
	for _, f := range out.Files {
		fmt.Printf("%10s  v%-3d %s  %s\n", humanBytes(f.Size), f.Version, f.Hash[:12], f.Path)
	}
	return nil
}

func cmdRm() error {
	if len(os.Args) < 3 {
		return fmt.Errorf("usage: badal rm <remote>")
	}
	c, err := loadCfg()
	if err != nil {
		return err
	}
	return c.json("DELETE", "/v1/blob/"+os.Args[2], nil, nil)
}

func cmdSync() error {
	c, err := loadCfg()
	if err != nil {
		return err
	}
	since := "0"
	if len(os.Args) > 2 {
		since = os.Args[2]
	}
	var out struct {
		Changes []struct {
			Seq        int64  `json:"seq"`
			EntityType string `json:"entity_type"`
			EntityID   string `json:"entity_id"`
			Op         string `json:"op"`
			HLC        string `json:"hlc"`
		} `json:"changes"`
		Cursor int64 `json:"cursor"`
		Head   int64 `json:"head"`
		More   bool  `json:"more"`
	}
	if err := c.json("GET", "/v1/sync?since="+since, nil, &out); err != nil {
		return err
	}
	for _, ch := range out.Changes {
		fmt.Printf("%6d  %-8s %-6s %s  %s\n", ch.Seq, ch.EntityType, ch.Op, ch.EntityID[:8], ch.HLC)
	}
	fmt.Printf("cursor=%d head=%d more=%v\n", out.Cursor, out.Head, out.More)
	return nil
}

func cmdDevices() error {
	c, err := loadCfg()
	if err != nil {
		return err
	}
	var out struct {
		Devices []map[string]any `json:"devices"`
	}
	if err := c.json("GET", "/v1/devices", nil, &out); err != nil {
		return err
	}
	for _, d := range out.Devices {
		state := "active"
		if d["revoked_at"] != nil {
			state = "REVOKED"
		}
		fmt.Printf("%-38s %-8s %-8s %v\n", d["id"], d["platform"], state, d["scopes"])
	}
	return nil
}

func humanBytes(n int64) string {
	const u = 1024
	if n < u {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(u), 0
	for m := n / u; m >= u; m /= u {
		div *= u
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
