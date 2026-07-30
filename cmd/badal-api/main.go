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
	"github.com/NandishwarSingh/badal/internal/calendar"
	"github.com/NandishwarSingh/badal/internal/chunks"
	"github.com/NandishwarSingh/badal/internal/config"
	"github.com/NandishwarSingh/badal/internal/extract"
	"github.com/NandishwarSingh/badal/internal/httpx"
	"github.com/NandishwarSingh/badal/internal/mail"
	"github.com/NandishwarSingh/badal/internal/notes"
	"github.com/NandishwarSingh/badal/internal/objects"
	"github.com/NandishwarSingh/badal/internal/passkey"
	"github.com/NandishwarSingh/badal/internal/push"
	"github.com/NandishwarSingh/badal/internal/secretbox"
	"github.com/NandishwarSingh/badal/internal/settings"
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

	setSvc := settings.New(st.Pool)

	// Mail is optional: without a secret key we cannot seal credentials, and
	// storing an app password in plaintext is not an acceptable fallback.
	var mailSvc *mail.Service
	if k := os.Getenv("BADAL_SECRET_KEY"); k != "" {
		box, err := secretbox.New(k)
		if err != nil {
			slog.Error("secret key", "err", err)
			os.Exit(1)
		}
		mailSvc = mail.New(st.Pool, obj, box)
		go mailSvc.Poll(ctx,
			time.Duration(setSvc.Int(ctx, "mail.poll_seconds", 120))*time.Second,
			setSvc.Int(ctx, "mail.backfill_days", 7),
			func(r map[string]mail.SyncResult) {
				for acct, res := range r {
					if res.Fetched > 0 || res.Error != "" {
						slog.Info("mail sync", "account", acct, "fetched", res.Fetched,
							"backfill", res.Backfill, "err", res.Error)
					}
				}
			})
		slog.Info("mail ingestion enabled")
	} else {
		slog.Warn("BADAL_SECRET_KEY unset; mail ingestion disabled")
	}

	calSvc := calendar.New(st.Pool)
	exSvc := extract.New(st.Pool, os.Getenv("BADAL_OPENROUTER_KEY"),
		os.Getenv("BADAL_EXTRACT_MODEL"), env("BADAL_TZ", "Asia/Kolkata"))
	if !exSvc.Enabled() {
		slog.Warn("BADAL_OPENROUTER_KEY unset; extraction disabled")
	}

	pushSvc, err := push.New(ctx, st.Pool, cfg.PushSubject)
	if err != nil {
		slog.Error("push init", "err", err)
		os.Exit(1)
	}

	// Daily digest. Local time, because a UTC "today" would report yesterday
	// evening's events at 06:00 IST.
	tz, tzErr := time.LoadLocation(env("BADAL_TZ", "Asia/Kolkata"))
	if tzErr != nil {
		tz = time.UTC
	}
	go func() {
		for {
			now := time.Now().In(tz)
			hour := setSvc.Int(ctx, "digest.hour", 7)
			next := time.Date(now.Year(), now.Month(), now.Day(), hour, 0, 0, 0, tz)
			if !next.After(now) {
				next = next.AddDate(0, 0, 1)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Until(next)):
			}
			if !setSvc.Bool(ctx, "digest.enabled", true) {
				continue
			}
			d, err := calSvc.BuildDigest(ctx, tz)
			if err != nil || d.Empty {
				continue
			}
			if n, err := pushSvc.Send(ctx, push.Message{
				Title: d.Title, Body: d.Body, URL: "/calendar", Tag: "digest",
			}); err != nil {
				slog.Warn("digest push", "err", err)
			} else {
				slog.Info("digest sent", "devices", n, "today", d.Today, "proposed", d.Proposed)
			}
		}
	}()

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

	// ---------------------------------------------------------------- mail
	mux.Handle("GET /v1/settings", authed(func(w http.ResponseWriter, r *http.Request) {
		all, err := setSvc.All(r.Context())
		if err != nil {
			httpx.Err(w, 500, "settings_failed", err.Error())
			return
		}
		httpx.JSON(w, 200, all)
	}))

	mux.Handle("PUT /v1/settings", authed(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.Err(w, 400, "bad_request", err.Error())
			return
		}
		for k, v := range req {
			if err := setSvc.Set(r.Context(), k, v); err != nil {
				httpx.Err(w, 500, "set_failed", err.Error())
				return
			}
		}
		all, _ := setSvc.All(r.Context())
		httpx.JSON(w, 200, all)
	}))

	if mailSvc != nil {
		mux.Handle("GET /v1/mail/accounts", authed(func(w http.ResponseWriter, r *http.Request) {
			list, err := mailSvc.Accounts(r.Context())
			if err != nil {
				httpx.Err(w, 500, "list_failed", err.Error())
				return
			}
			httpx.JSON(w, 200, map[string]any{"accounts": list})
		}))

		mux.Handle("POST /v1/mail/accounts", authed(func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				Email    string `json:"email"`
				Host     string `json:"imap_host"`
				Port     int    `json:"imap_port"`
				Username string `json:"username"`
				Password string `json:"password"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				httpx.Err(w, 400, "bad_request", err.Error())
				return
			}
			// Gmail shows app passwords in groups of four; people paste the spaces.
			req.Password = strings.ReplaceAll(req.Password, " ", "")
			a, err := mailSvc.AddAccount(r.Context(), req.Email, req.Host, req.Port, req.Username, req.Password)
			if err != nil {
				httpx.Err(w, 400, "add_failed", err.Error())
				return
			}
			httpx.JSON(w, 201, a)
		}))

		mux.Handle("DELETE /v1/mail/accounts/{id}", authed(func(w http.ResponseWriter, r *http.Request) {
			id, err := uuid.Parse(r.PathValue("id"))
			if err != nil {
				httpx.Err(w, 400, "bad_request", "invalid id")
				return
			}
			if err := mailSvc.DeleteAccount(r.Context(), id); err != nil {
				httpx.Err(w, 500, "delete_failed", err.Error())
				return
			}
			httpx.JSON(w, 200, map[string]any{"deleted": id})
		}))

		// Preview the digest without waiting for the scheduled hour.
		mux.Handle("GET /v1/digest", authed(func(w http.ResponseWriter, r *http.Request) {
			d, err := calSvc.BuildDigest(r.Context(), tz)
			if err != nil {
				httpx.Err(w, 500, "digest_failed", err.Error())
				return
			}
			if r.URL.Query().Get("send") == "1" && !d.Empty {
				n, _ := pushSvc.Send(r.Context(), push.Message{
					Title: d.Title, Body: d.Body, URL: "/calendar", Tag: "digest"})
				httpx.JSON(w, 200, map[string]any{"digest": d, "sent": n})
				return
			}
			httpx.JSON(w, 200, map[string]any{"digest": d})
		}))

		mux.Handle("POST /v1/mail/sync", authed(func(w http.ResponseWriter, r *http.Request) {
			res := mailSvc.SyncAll(r.Context(), setSvc.Int(r.Context(), "mail.backfill_days", 7))
			httpx.JSON(w, 200, map[string]any{"results": res})
		}))

		// Single message with its body, so the event sheet can show the source
		// without the list endpoint having to carry every body.
		mux.Handle("GET /v1/mail/messages/{id}", authed(func(w http.ResponseWriter, r *http.Request) {
			id, err := uuid.Parse(r.PathValue("id"))
			if err != nil {
				httpx.Err(w, 400, "bad_request", "invalid id")
				return
			}
			m, err := mailSvc.Message(r.Context(), id)
			if err != nil {
				httpx.Err(w, 404, "not_found", err.Error())
				return
			}
			httpx.JSON(w, 200, m)
		}))

		mux.Handle("GET /v1/mail/messages", authed(func(w http.ResponseWriter, r *http.Request) {
			limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
			list, err := mailSvc.List(r.Context(), limit)
			if err != nil {
				httpx.Err(w, 500, "list_failed", err.Error())
				return
			}
			httpx.JSON(w, 200, map[string]any{"messages": list, "count": len(list)})
		}))
	}

	// ------------------------------------------------------------ calendar
	mux.Handle("GET /v1/events", authed(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		from, to := monthWindow(q.Get("from"), q.Get("to"))
		list, err := calSvc.Range(r.Context(), from, to, q.Get("proposed") != "0")
		if err != nil {
			httpx.Err(w, 500, "range_failed", err.Error())
			return
		}
		counts, _ := calSvc.Counts(r.Context())
		httpx.JSON(w, 200, map[string]any{
			"events": list, "from": from, "to": to, "counts": counts})
	}))

	mux.Handle("GET /v1/events/proposed", authed(func(w http.ResponseWriter, r *http.Request) {
		list, err := calSvc.Proposed(r.Context(), 200)
		if err != nil {
			httpx.Err(w, 500, "proposed_failed", err.Error())
			return
		}
		httpx.JSON(w, 200, map[string]any{"events": list, "count": len(list)})
	}))

	mux.Handle("POST /v1/events", authed(func(w http.ResponseWriter, r *http.Request) {
		var e calendar.Event
		if err := json.NewDecoder(r.Body).Decode(&e); err != nil || e.Title == "" {
			httpx.Err(w, 400, "bad_request", "title and starts_at are required")
			return
		}
		out, err := calSvc.Create(r.Context(), e)
		if err != nil {
			httpx.Err(w, 400, "create_failed", err.Error())
			return
		}
		httpx.JSON(w, 201, out)
	}))

	mux.Handle("POST /v1/events/{id}/state", authed(func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			httpx.Err(w, 400, "bad_request", "invalid id")
			return
		}
		var req struct {
			State string `json:"state"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		out, err := calSvc.SetState(r.Context(), id, req.State)
		if err != nil {
			httpx.Err(w, 400, "state_failed", err.Error())
			return
		}
		httpx.JSON(w, 200, out)
	}))

	mux.Handle("POST /v1/events/confirm-all", authed(func(w http.ResponseWriter, r *http.Request) {
		n, err := calSvc.ConfirmAll(r.Context(), 0)
		if err != nil {
			httpx.Err(w, 500, "confirm_failed", err.Error())
			return
		}
		httpx.JSON(w, 200, map[string]any{"confirmed": n})
	}))

	mux.Handle("PATCH /v1/events/{id}", authed(func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			httpx.Err(w, 400, "bad_request", "invalid id")
			return
		}
		var p calendar.Patch
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			httpx.Err(w, 400, "bad_request", err.Error())
			return
		}
		out, err := calSvc.Update(r.Context(), id, p)
		if err != nil {
			httpx.Err(w, 400, "update_failed", err.Error())
			return
		}
		httpx.JSON(w, 200, out)
	}))

	mux.Handle("DELETE /v1/events/{id}", authed(func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			httpx.Err(w, 400, "bad_request", "invalid id")
			return
		}
		if err := calSvc.Delete(r.Context(), id); err != nil {
			httpx.Err(w, 500, "delete_failed", err.Error())
			return
		}
		httpx.JSON(w, 200, map[string]any{"deleted": id})
	}))

	// Run extraction over whatever has not been processed at this version.
	if mailSvc != nil && exSvc.Enabled() {
		mux.Handle("POST /v1/mail/extract", authed(func(w http.ResponseWriter, r *http.Request) {
			limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
			if limit <= 0 {
				limit = 25
			}
			pending, err := mailSvc.Pending(r.Context(), extract.Version, limit)
			if err != nil {
				httpx.Err(w, 500, "pending_failed", err.Error())
				return
			}
			auto := setSvc.Bool(r.Context(), "mail.auto_confirm", false)
			minConf := setSvc.Float(r.Context(), "mail.auto_confirm_min", 0.85)

			results := []extract.Result{}
			for _, m := range pending {
				body := m.BodyText
				// PDF circulars often carry the only copy of the date. Failure
				// here is not fatal -- the message body is still extracted.
				if txt, err := mailSvc.AttachmentText(r.Context(), m.ID, 6000); err == nil && txt != "" {
					body += "\n\n[ATTACHMENTS]\n" + txt
				}
				res, err := exSvc.Extract(r.Context(), extract.Message{
					ID: m.ID, Subject: m.Subject, FromName: m.FromName,
					FromAddr: m.FromAddr, ReceivedAt: m.ReceivedAt, Body: body,
				}, auto, minConf)
				errStr := ""
				if err != nil {
					errStr = err.Error()
				}
				_ = mailSvc.MarkExtracted(r.Context(), m.ID, extract.Version, errStr)
				results = append(results, res)
			}
			kept := 0
			for _, r := range results {
				kept += r.Kept
			}
			httpx.JSON(w, 200, map[string]any{
				"processed": len(results), "events": kept, "results": results})
		}))
	}

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
			// Nothing was lost: the edit is parked on the note and the client
			// is handed the current state plus the open conflicts to diff.
			conf, _ := noteSvc.ConflictsFor(r.Context(), n.ID)
			httpx.JSON(w, http.StatusConflict, map[string]any{
				"error": "version_conflict", "note": n, "conflicts": conf,
				"detail": "the note changed since you loaded it; your edit is kept for review",
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
		conf, _ := noteSvc.ConflictsFor(r.Context(), n.ID)
		httpx.JSON(w, 200, map[string]any{
			"note": n, "backlinks": back, "outlinks": out, "conflicts": conf})
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
		list, err := noteSvc.OpenConflicts(r.Context())
		if err != nil {
			httpx.Err(w, 500, "conflicts_failed", err.Error())
			return
		}
		httpx.JSON(w, 200, map[string]any{"conflicts": list, "count": len(list)})
	}))

	// Resolve: keep the server's text, take the rejected edit, or save a
	// version the user assembled from both.
	mux.Handle("POST /v1/conflicts/{id}/resolve", authed(func(w http.ResponseWriter, r *http.Request) {
		d, _ := auth.DeviceFrom(r.Context())
		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			httpx.Err(w, 400, "bad_request", "invalid conflict id")
			return
		}
		var req struct {
			Keep  string   `json:"keep"` // current | incoming | custom
			Title string   `json:"title"`
			Body  string   `json:"body"`
			Tags  []string `json:"tags"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.Err(w, 400, "bad_request", err.Error())
			return
		}
		var custom *notes.PutInput
		if req.Keep == "custom" {
			custom = &notes.PutInput{Title: req.Title, Body: req.Body, Tags: req.Tags}
		}
		n, err := noteSvc.ResolveConflict(r.Context(), d.ID, id, req.Keep, custom)
		if errors.Is(err, notes.ErrNotFound) {
			httpx.Err(w, 404, "not_found", "conflict already resolved")
			return
		}
		if err != nil {
			httpx.Err(w, 400, "resolve_failed", err.Error())
			return
		}
		httpx.JSON(w, 200, map[string]any{"note": n, "resolved": req.Keep})
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

// monthWindow parses the requested range, defaulting to the current month plus
// a generous margin so the client can render adjacent weeks without a refetch.
func monthWindow(fromS, toS string) (time.Time, time.Time) {
	now := time.Now()
	from := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -7)
	to := from.AddDate(0, 2, 14)
	if fromS != "" {
		if t, err := time.Parse(time.RFC3339, fromS); err == nil {
			from = t
		}
	}
	if toS != "" {
		if t, err := time.Parse(time.RFC3339, toS); err == nil {
			to = t
		}
	}
	return from, to
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
