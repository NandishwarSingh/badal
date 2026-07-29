// Command badal-api is the badal control plane.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/NandishwarSingh/badal/internal/auth"
	"github.com/NandishwarSingh/badal/internal/blobs"
	"github.com/NandishwarSingh/badal/internal/chunks"
	"github.com/NandishwarSingh/badal/internal/config"
	"github.com/NandishwarSingh/badal/internal/httpx"
	"github.com/NandishwarSingh/badal/internal/objects"
	"github.com/NandishwarSingh/badal/internal/store"
	"github.com/NandishwarSingh/badal/internal/syncfeed"
)

const maxChunkBytes = 8 << 20 // headroom over the 4MB FastCDC max

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}
	ctx := context.Background()

	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("database", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	applied, err := st.Migrate(ctx)
	if err != nil {
		slog.Error("migrate", "err", err)
		os.Exit(1)
	}
	if len(applied) > 0 {
		slog.Info("migrations applied", "files", applied)
	}

	obj, err := objects.Open(ctx, cfg.S3Endpoint, cfg.S3KeyID, cfg.S3Secret, cfg.S3Region, cfg.S3Bucket, cfg.S3UseSSL)
	if err != nil {
		slog.Error("object store", "err", err)
		os.Exit(1)
	}

	hostname, _ := os.Hostname()
	feed := syncfeed.New(st.Pool, hostname)
	authSvc := auth.New(st.Pool)
	blobSvc := blobs.New(st.Pool, obj, feed)
	bootstrap := os.Getenv("BADAL_BOOTSTRAP_TOKEN")

	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		if err := st.Pool.Ping(r.Context()); err != nil {
			httpx.Err(w, http.StatusServiceUnavailable, "db_unavailable", err.Error())
			return
		}
		httpx.JSON(w, 200, map[string]any{"status": "ok", "time": time.Now().UTC()})
	})

	// ------------------------------------------------------------ devices
	// Registration accepts either the bootstrap token (to create the very
	// first device) or an existing full-scope device token.
	mux.HandleFunc("POST /v1/devices", func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		allowed := bootstrap != "" && tok == bootstrap
		if !allowed {
			if d, err := authSvc.Authenticate(r.Context(), tok); err == nil {
				for _, s := range d.Scopes {
					if s == "full" {
						allowed = true
					}
				}
			}
		}
		if !allowed {
			httpx.Err(w, http.StatusUnauthorized, "unauthorized", "bootstrap or full-scope token required")
			return
		}
		var req struct {
			Name     string   `json:"name"`
			Platform string   `json:"platform"`
			Scopes   []string `json:"scopes"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" || req.Platform == "" {
			httpx.Err(w, http.StatusBadRequest, "bad_request", "name and platform are required")
			return
		}
		d, plaintext, err := authSvc.Register(r.Context(), req.Name, req.Platform, req.Scopes)
		if err != nil {
			httpx.Err(w, http.StatusInternalServerError, "register_failed", err.Error())
			return
		}
		// Shown exactly once; only its hash is stored.
		httpx.JSON(w, 201, map[string]any{"device": d, "token": plaintext})
	})

	authed := func(h http.HandlerFunc) http.Handler { return authSvc.Middleware(h) }

	mux.Handle("GET /v1/devices", authed(func(w http.ResponseWriter, r *http.Request) {
		list, err := authSvc.List(r.Context())
		if err != nil {
			httpx.Err(w, 500, "list_failed", err.Error())
			return
		}
		httpx.JSON(w, 200, map[string]any{"devices": list})
	}))

	mux.Handle("DELETE /v1/devices/{id}", authed(func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			httpx.Err(w, 400, "bad_request", "invalid uuid")
			return
		}
		if err := authSvc.Revoke(r.Context(), id); err != nil {
			httpx.Err(w, 500, "revoke_failed", err.Error())
			return
		}
		httpx.JSON(w, 200, map[string]any{"revoked": id})
	}))

	// ------------------------------------------------------------- chunks
	mux.Handle("POST /v1/chunks/probe", authed(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Hashes []string `json:"hashes"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.Err(w, 400, "bad_request", err.Error())
			return
		}
		raw := make([][]byte, 0, len(req.Hashes))
		for _, h := range req.Hashes {
			b, err := chunks.FromHex(h)
			if err != nil {
				httpx.Err(w, 400, "bad_request", "invalid hash: "+h)
				return
			}
			raw = append(raw, b)
		}
		missing, err := blobSvc.Probe(r.Context(), raw)
		if err != nil {
			httpx.Err(w, 500, "probe_failed", err.Error())
			return
		}
		out := make([]string, len(missing))
		for i, m := range missing {
			out[i] = chunks.Hex(m)
		}
		httpx.JSON(w, 200, map[string]any{"missing": out, "have": len(req.Hashes) - len(out)})
	}))

	mux.Handle("PUT /v1/chunks/{hash}", authed(func(w http.ResponseWriter, r *http.Request) {
		hash, err := chunks.FromHex(r.PathValue("hash"))
		if err != nil || len(hash) != 32 {
			httpx.Err(w, 400, "bad_request", "hash must be 64 hex chars")
			return
		}
		data, err := io.ReadAll(io.LimitReader(r.Body, maxChunkBytes+1))
		if err != nil {
			httpx.Err(w, 400, "read_failed", err.Error())
			return
		}
		if len(data) > maxChunkBytes {
			httpx.Err(w, http.StatusRequestEntityTooLarge, "chunk_too_large", "")
			return
		}
		if err := blobSvc.PutChunk(r.Context(), hash, data); err != nil {
			httpx.Err(w, 400, "put_chunk_failed", err.Error())
			return
		}
		httpx.JSON(w, 201, map[string]any{"hash": chunks.Hex(hash), "size": len(data)})
	}))

	// -------------------------------------------------------------- files
	mux.Handle("POST /v1/files", authed(func(w http.ResponseWriter, r *http.Request) {
		d, _ := auth.DeviceFrom(r.Context())
		var req struct {
			Path        string   `json:"path"`
			Hash        string   `json:"hash"`
			Size        int64    `json:"size"`
			Chunks      []string `json:"chunks"`
			ContentType string   `json:"content_type"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.Err(w, 400, "bad_request", err.Error())
			return
		}
		if req.Path == "" || req.Hash == "" || len(req.Chunks) == 0 {
			httpx.Err(w, 400, "bad_request", "path, hash and chunks are required")
			return
		}
		whole, err := chunks.FromHex(req.Hash)
		if err != nil {
			httpx.Err(w, 400, "bad_request", "invalid hash")
			return
		}
		list := make([][]byte, 0, len(req.Chunks))
		for _, h := range req.Chunks {
			b, err := chunks.FromHex(h)
			if err != nil {
				httpx.Err(w, 400, "bad_request", "invalid chunk hash")
				return
			}
			list = append(list, b)
		}
		ct := req.ContentType
		if ct == "" {
			ct = "application/octet-stream"
		}
		f, err := blobSvc.Commit(r.Context(), d.ID, req.Path, whole, req.Size, list, ct)
		if err != nil {
			if errors.Is(err, blobs.ErrMissingChunk) {
				httpx.Err(w, http.StatusPreconditionFailed, "missing_chunks", err.Error())
				return
			}
			httpx.Err(w, 500, "commit_failed", err.Error())
			return
		}
		httpx.JSON(w, 201, f)
	}))

	mux.Handle("GET /v1/files", authed(func(w http.ResponseWriter, r *http.Request) {
		list, err := blobSvc.List(r.Context(), r.URL.Query().Get("prefix"))
		if err != nil {
			httpx.Err(w, 500, "list_failed", err.Error())
			return
		}
		httpx.JSON(w, 200, map[string]any{"files": list, "count": len(list)})
	}))

	mux.Handle("GET /v1/blob/{path...}", authed(func(w http.ResponseWriter, r *http.Request) {
		path := r.PathValue("path")
		meta, err := blobSvc.Open(r.Context(), path, nil)
		if errors.Is(err, blobs.ErrNotFound) {
			httpx.Err(w, 404, "not_found", path)
			return
		}
		if errors.Is(err, blobs.ErrEvicted) {
			// Archive lives on an intermittent machine; a cold read is a
			// queued restore, not a failure.
			httpx.JSON(w, http.StatusAccepted, map[string]any{
				"status": "restoring_from_archive", "path": path, "size": meta.Size})
			return
		}
		if err != nil {
			httpx.Err(w, 500, "open_failed", err.Error())
			return
		}
		w.Header().Set("Content-Type", meta.ContentType)
		w.Header().Set("Content-Length", strconv.FormatInt(meta.Size, 10))
		w.Header().Set("ETag", `"`+meta.Hash+`"`)
		if _, err := blobSvc.Open(r.Context(), path, w); err != nil {
			slog.Error("stream failed", "path", path, "err", err)
		}
	}))

	mux.Handle("DELETE /v1/blob/{path...}", authed(func(w http.ResponseWriter, r *http.Request) {
		d, _ := auth.DeviceFrom(r.Context())
		err := blobSvc.Delete(r.Context(), d.ID, r.PathValue("path"))
		if errors.Is(err, blobs.ErrNotFound) {
			httpx.Err(w, 404, "not_found", "")
			return
		}
		if err != nil {
			httpx.Err(w, 500, "delete_failed", err.Error())
			return
		}
		httpx.JSON(w, 200, map[string]any{"deleted": r.PathValue("path")})
	}))

	// --------------------------------------------------------------- sync
	mux.Handle("GET /v1/sync", authed(func(w http.ResponseWriter, r *http.Request) {
		since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		changes, head, err := feed.Since(r.Context(), since, limit)
		if err != nil {
			httpx.Err(w, 500, "sync_failed", err.Error())
			return
		}
		cursor := since
		if n := len(changes); n > 0 {
			cursor = changes[n-1].Seq
		}
		httpx.JSON(w, 200, map[string]any{
			"changes": changes, "cursor": cursor, "head": head, "more": cursor < head})
	}))

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           httpx.Recover(httpx.Logger(mux)),
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      5 * time.Minute, // large file streams
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		slog.Info("badal-api listening", "addr", cfg.Addr, "bucket", cfg.S3Bucket)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("listen", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	slog.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
}
