// Command badal-api is the badal control plane.
package main

import (
	"context"
	"crypto/subtle"
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
	"github.com/NandishwarSingh/badal/internal/notes"
	"github.com/NandishwarSingh/badal/internal/objects"
	"github.com/NandishwarSingh/badal/internal/passkey"
	"github.com/NandishwarSingh/badal/internal/push"
	"github.com/NandishwarSingh/badal/internal/store"
	"github.com/NandishwarSingh/badal/internal/surreal"
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
	noteSvc := notes.New(st.Pool, feed)
	bootstrap := os.Getenv("BADAL_BOOTSTRAP_TOKEN")
	enrolSecret := os.Getenv("BADAL_ENROL_SECRET")

	// Passkeys need the exact origin the PWA is served from.
	var pk *passkey.Service
	if cfg.RPID != "" && cfg.Origin != "" {
		pk, err = passkey.New(st.Pool, cfg.RPID, cfg.Origin)
		if err != nil {
			slog.Error("passkey init", "err", err)
			os.Exit(1)
		}
		go func() {
			t := time.NewTicker(time.Hour)
			defer t.Stop()
			for range t.C {
				pk.GC(context.Background())
			}
		}()
		slog.Info("passkeys enabled", "rpid", cfg.RPID, "origin", cfg.Origin)
	}

	pushSvc, err := push.New(ctx, st.Pool, cfg.PushSubject)
	if err != nil {
		slog.Error("push init", "err", err)
		os.Exit(1)
	}

	// SurrealDB is optional. It holds only derived data, so if it is down or
	// unconfigured the notes API keeps working and the projector catches up
	// later from its stored cursor.
	var projector *surreal.Projector
	if cfg.SurrealURL != "" {
		sc := surreal.NewClient(cfg.SurrealURL, cfg.SurrealUser, cfg.SurrealPass, cfg.SurrealNS, cfg.SurrealDB)
		if err := sc.Health(ctx); err != nil {
			slog.Warn("surrealdb unreachable; graph projection disabled", "err", err)
		} else if err := sc.EnsureSchema(ctx); err != nil {
			slog.Warn("surrealdb schema failed; graph projection disabled", "err", err)
		} else {
			projector = surreal.NewProjector(st.Pool, sc, noteSvc)
			go projector.Run(ctx, 5*time.Second)
			slog.Info("surrealdb projector started", "ns", cfg.SurrealNS, "db", cfg.SurrealDB)
		}
	}

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

	// Two credential types reach the same endpoints: bearer tokens for the CLI
	// and worker, httpOnly session cookies for the browser. The browser never
	// holds a long-lived token, so an XSS has nothing durable to steal.
	authed := func(h http.HandlerFunc) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tok := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
			if tok != "" {
				if d, err := authSvc.Authenticate(r.Context(), tok); err == nil {
					h(w, r.WithContext(auth.WithDevice(r.Context(), d)))
					return
				}
			}
			if pk != nil {
				if c, err := r.Cookie(passkey.SessionCookie); err == nil {
					if devID, err := pk.Authenticate(r.Context(), c.Value); err == nil {
						h(w, r.WithContext(auth.WithDevice(r.Context(),
							auth.Device{ID: devID, Name: "session", Platform: "web", Scopes: []string{"full"}})))
						return
					}
				}
			}
			httpx.Err(w, http.StatusUnauthorized, "unauthorized", "")
		})
	}

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

	// Diagnostic: reports what the server actually received, without echoing
	// the credential itself. Cheaper than guessing why a phone sees a 401.
	mux.HandleFunc("GET /v1/auth/echo", func(w http.ResponseWriter, r *http.Request) {
		raw := r.Header.Get("Authorization")
		tok := strings.TrimSpace(strings.TrimPrefix(raw, "Bearer "))
		out := map[string]any{
			"authorization_header_present": raw != "",
			"header_raw_len":               len(raw),
			"token_len":                    len(tok),
			"token_prefix":                 firstN(tok, 6),
			"token_suffix":                 lastN(tok, 4),
			"has_session_cookie":           false,
			"token_accepted":               false,
			"origin":                       r.Header.Get("Origin"),
			"host":                         r.Host,
		}
		if c, err := r.Cookie(passkey.SessionCookie); err == nil && c.Value != "" {
			out["has_session_cookie"] = true
		}
		if tok != "" {
			if _, err := authSvc.Authenticate(r.Context(), tok); err == nil {
				out["token_accepted"] = true
			}
		}
		httpx.JSON(w, 200, out)
	})

	// ------------------------------------------------------------ passkeys
	if pk != nil {
		// Whether to show "create a passkey" or "sign in". Unauthenticated by
		// necessity -- it leaks only whether any passkey exists.
		mux.HandleFunc("GET /v1/auth/state", func(w http.ResponseWriter, r *http.Request) {
			has, _ := pk.HasCredentials(r.Context())
			signedIn := false
			if c, err := r.Cookie(passkey.SessionCookie); err == nil {
				_, e := pk.Authenticate(r.Context(), c.Value)
				signedIn = e == nil
			}
			httpx.JSON(w, 200, map[string]any{"has_passkeys": has, "signed_in": signedIn})
		})

		// First passkey is claimed with the bootstrap-style enrolment secret or
		// an existing session; after that, only an existing session can add one.
		// Otherwise anyone reaching the URL could enrol themselves.
		mux.HandleFunc("POST /v1/auth/register/begin", func(w http.ResponseWriter, r *http.Request) {
			if !mayEnrol(r, pk, authSvc, enrolSecret) {
				httpx.Err(w, 401, "unauthorized", "enrolment secret or an existing session is required")
				return
			}
			opts, id, err := pk.BeginRegister(r.Context())
			if err != nil {
				httpx.Err(w, 500, "begin_failed", err.Error())
				return
			}
			httpx.JSON(w, 200, map[string]any{"ceremony": id, "options": opts})
		})

		mux.HandleFunc("POST /v1/auth/register/finish", func(w http.ResponseWriter, r *http.Request) {
			if !mayEnrol(r, pk, authSvc, enrolSecret) {
				httpx.Err(w, 401, "unauthorized", "")
				return
			}
			id, err := uuid.Parse(r.URL.Query().Get("ceremony"))
			if err != nil {
				httpx.Err(w, 400, "bad_request", "missing ceremony")
				return
			}
			if err := pk.FinishRegister(r.Context(), id, r.URL.Query().Get("label"), r); err != nil {
				httpx.Err(w, 400, "register_failed", err.Error())
				return
			}
			httpx.JSON(w, 200, map[string]any{"registered": true})
		})

		mux.HandleFunc("POST /v1/auth/login/begin", func(w http.ResponseWriter, r *http.Request) {
			opts, id, err := pk.BeginLogin(r.Context())
			if err != nil {
				httpx.Err(w, 400, "begin_failed", err.Error())
				return
			}
			httpx.JSON(w, 200, map[string]any{"ceremony": id, "options": opts})
		})

		mux.HandleFunc("POST /v1/auth/login/finish", func(w http.ResponseWriter, r *http.Request) {
			id, err := uuid.Parse(r.URL.Query().Get("ceremony"))
			if err != nil {
				httpx.Err(w, 400, "bad_request", "missing ceremony")
				return
			}
			tok, exp, err := pk.FinishLogin(r.Context(), id, r)
			if err != nil {
				httpx.Err(w, 401, "login_failed", err.Error())
				return
			}
			passkey.SetCookie(w, tok, exp)
			httpx.JSON(w, 200, map[string]any{"signed_in": true, "expires_at": exp})
		})

		mux.HandleFunc("POST /v1/auth/logout", func(w http.ResponseWriter, r *http.Request) {
			if c, err := r.Cookie(passkey.SessionCookie); err == nil {
				_ = pk.Logout(r.Context(), c.Value)
			}
			passkey.ClearCookie(w)
			httpx.JSON(w, 200, map[string]any{"signed_out": true})
		})

		mux.Handle("GET /v1/auth/credentials", authed(func(w http.ResponseWriter, r *http.Request) {
			list, err := pk.ListCredentials(r.Context())
			if err != nil {
				httpx.Err(w, 500, "list_failed", err.Error())
				return
			}
			httpx.JSON(w, 200, map[string]any{"credentials": list})
		}))

		mux.Handle("DELETE /v1/auth/credentials/{id}", authed(func(w http.ResponseWriter, r *http.Request) {
			if err := pk.DeleteCredential(r.Context(), r.PathValue("id")); err != nil {
				httpx.Err(w, 400, "delete_failed", err.Error())
				return
			}
			httpx.JSON(w, 200, map[string]any{"deleted": true})
		}))
	}

	// ---------------------------------------------------------------- push
	mux.HandleFunc("GET /v1/push/key", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, 200, map[string]any{"public_key": pushSvc.PublicKey()})
	})

	mux.Handle("POST /v1/push/subscribe", authed(func(w http.ResponseWriter, r *http.Request) {
		d, _ := auth.DeviceFrom(r.Context())
		var sub push.Subscription
		if err := json.NewDecoder(r.Body).Decode(&sub); err != nil {
			httpx.Err(w, 400, "bad_request", err.Error())
			return
		}
		if err := pushSvc.Subscribe(r.Context(), sub, &d.ID, r.UserAgent()); err != nil {
			httpx.Err(w, 400, "subscribe_failed", err.Error())
			return
		}
		httpx.JSON(w, 201, map[string]any{"subscribed": true})
	}))

	mux.Handle("POST /v1/push/unsubscribe", authed(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Endpoint string `json:"endpoint"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		_ = pushSvc.Unsubscribe(r.Context(), req.Endpoint)
		httpx.JSON(w, 200, map[string]any{"unsubscribed": true})
	}))

	mux.Handle("POST /v1/push/test", authed(func(w http.ResponseWriter, r *http.Request) {
		n, err := pushSvc.Send(r.Context(), push.Message{
			Title: "badal", Body: "Push is working.", URL: "/",
		})
		if err != nil {
			httpx.Err(w, 500, "send_failed", err.Error())
			return
		}
		total, _ := pushSvc.Count(r.Context())
		httpx.JSON(w, 200, map[string]any{"sent": n, "subscriptions": total})
	}))

	// --------------------------------------------------------------- notes
	mux.Handle("GET /v1/notes", authed(func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		list, err := noteSvc.List(r.Context(), notes.ListOpts{Tag: r.URL.Query().Get("tag"), Limit: limit})
		if err != nil {
			httpx.Err(w, 500, "list_failed", err.Error())
			return
		}
		httpx.JSON(w, 200, map[string]any{"notes": list, "count": len(list)})
	}))

	// PUT is the single write path: it creates when absent and updates when
	// present, so an offline client replaying its outbox needs no branching.
	mux.Handle("PUT /v1/notes/{slug}", authed(func(w http.ResponseWriter, r *http.Request) {
		d, _ := auth.DeviceFrom(r.Context())
		var req struct {
			Title   string   `json:"title"`
			Body    string   `json:"body"`
			Tags    []string `json:"tags"`
			Version *int     `json:"version"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.Err(w, 400, "bad_request", err.Error())
			return
		}
		n, err := noteSvc.Put(r.Context(), d.ID, notes.PutInput{
			Slug: r.PathValue("slug"), Title: req.Title, Body: req.Body,
			Tags: req.Tags, Version: req.Version,
		})
		if errors.Is(err, notes.ErrConflict) {
			// Nothing was lost: the incoming edit became a conflict copy and
			// the client is told where to find it.
			httpx.JSON(w, http.StatusConflict, map[string]any{
				"error": "version_conflict", "conflict_copy": n,
				"detail": "your edit was saved as a conflict copy; the note had changed since you loaded it",
			})
			return
		}
		if err != nil {
			httpx.Err(w, 400, "put_failed", err.Error())
			return
		}
		httpx.JSON(w, 200, n)
	}))

	mux.Handle("GET /v1/notes/{slug}", authed(func(w http.ResponseWriter, r *http.Request) {
		n, err := noteSvc.Get(r.Context(), r.PathValue("slug"))
		if errors.Is(err, notes.ErrNotFound) {
			httpx.Err(w, 404, "not_found", r.PathValue("slug"))
			return
		}
		if err != nil {
			httpx.Err(w, 500, "get_failed", err.Error())
			return
		}
		back, _ := noteSvc.Backlinks(r.Context(), n.Slug)
		out, _ := noteSvc.Outlinks(r.Context(), n.ID)
		httpx.JSON(w, 200, map[string]any{"note": n, "backlinks": back, "outlinks": out})
	}))

	mux.Handle("DELETE /v1/notes/{slug}", authed(func(w http.ResponseWriter, r *http.Request) {
		d, _ := auth.DeviceFrom(r.Context())
		err := noteSvc.Delete(r.Context(), d.ID, r.PathValue("slug"))
		if errors.Is(err, notes.ErrNotFound) {
			httpx.Err(w, 404, "not_found", "")
			return
		}
		if err != nil {
			httpx.Err(w, 500, "delete_failed", err.Error())
			return
		}
		httpx.JSON(w, 200, map[string]any{"deleted": r.PathValue("slug")})
	}))

	mux.Handle("GET /v1/search", authed(func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		hits, err := noteSvc.Search(r.Context(), r.URL.Query().Get("q"), limit)
		if err != nil {
			httpx.Err(w, 500, "search_failed", err.Error())
			return
		}
		httpx.JSON(w, 200, map[string]any{"hits": hits, "count": len(hits)})
	}))

	mux.Handle("GET /v1/conflicts", authed(func(w http.ResponseWriter, r *http.Request) {
		list, err := noteSvc.Conflicts(r.Context())
		if err != nil {
			httpx.Err(w, 500, "conflicts_failed", err.Error())
			return
		}
		httpx.JSON(w, 200, map[string]any{"conflicts": list, "count": len(list)})
	}))

	mux.Handle("GET /v1/notes/{slug}/related", authed(func(w http.ResponseWriter, r *http.Request) {
		if projector == nil {
			httpx.Err(w, http.StatusServiceUnavailable, "graph_unavailable", "surrealdb not configured")
			return
		}
		n, err := noteSvc.Get(r.Context(), r.PathValue("slug"))
		if err != nil {
			httpx.Err(w, 404, "not_found", r.PathValue("slug"))
			return
		}
		depth, _ := strconv.Atoi(r.URL.Query().Get("depth"))
		res, err := projector.Related(r.Context(), n.ID, depth)
		if err != nil {
			httpx.Err(w, 502, "graph_failed", err.Error())
			return
		}
		httpx.Raw(w, 200, res)
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

// mayEnrol gates passkey registration. Without this any visitor could enrol
// their own passkey and own the instance -- the classic "first user wins"
// hole in single-tenant WebAuthn setups.
func mayEnrol(r *http.Request, pk *passkey.Service, a *auth.Service, secret string) bool {
	if c, err := r.Cookie(passkey.SessionCookie); err == nil {
		if _, err := pk.Authenticate(r.Context(), c.Value); err == nil {
			return true
		}
	}
	tok := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if tok != "" {
		if _, err := a.Authenticate(r.Context(), tok); err == nil {
			return true
		}
	}
	if secret != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Badal-Enrol")), []byte(secret)) == 1 {
		return true
	}
	return false
}

func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func lastN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
