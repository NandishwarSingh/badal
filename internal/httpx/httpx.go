// Package httpx holds the small HTTP conveniences shared by every handler.
package httpx

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

type ErrorBody struct {
	Error  string `json:"error"`
	Detail string `json:"detail,omitempty"`
}

func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func Err(w http.ResponseWriter, status int, msg, detail string) {
	JSON(w, status, ErrorBody{Error: msg, Detail: detail})
}

// Logger records method, path, status and duration for every request.
type statusWriter struct {
	http.ResponseWriter
	status int
	n      int64
}

func (s *statusWriter) WriteHeader(c int) { s.status = c; s.ResponseWriter.WriteHeader(c) }
func (s *statusWriter) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = 200
	}
	n, err := s.ResponseWriter.Write(b)
	s.n += int64(n)
	return n, err
}

func Logger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		if sw.status == 0 {
			sw.status = 200
		}
		slog.Info("http",
			"method", r.Method, "path", r.URL.Path, "status", sw.status,
			"bytes", sw.n, "dur", time.Since(start).Round(time.Millisecond).String())
	})
}

// Recover turns a panic into a 500 instead of killing the connection.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				slog.Error("panic", "err", v, "path", r.URL.Path)
				Err(w, http.StatusInternalServerError, "internal_error", "")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
