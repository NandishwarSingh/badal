// Package auth implements device registration and opaque bearer tokens.
//
// Tokens are random 256-bit values shown exactly once at registration. Only
// their SHA-256 is stored, so a database leak does not yield usable
// credentials. Each device is independently revocable -- which you want the
// first time a device goes missing.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrUnauthorized = errors.New("unauthorized")

type Device struct {
	ID       uuid.UUID `json:"id"`
	Name     string    `json:"name"`
	Platform string    `json:"platform"`
	Scopes   []string  `json:"scopes"`
}

type Service struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

func hash(tok string) []byte { s := sha256.Sum256([]byte(tok)); return s[:] }

// Register creates a device and returns the plaintext token exactly once.
func (s *Service) Register(ctx context.Context, name, platform string, scopes []string) (Device, string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return Device{}, "", err
	}
	tok := "bdl_" + base64.RawURLEncoding.EncodeToString(raw)
	if len(scopes) == 0 {
		scopes = []string{"full"}
	}
	var d Device
	err := s.pool.QueryRow(ctx,
		`INSERT INTO devices (name, platform, token_hash, scopes)
		 VALUES ($1,$2,$3,$4) RETURNING id, name, platform, scopes`,
		name, platform, hash(tok), scopes,
	).Scan(&d.ID, &d.Name, &d.Platform, &d.Scopes)
	if err != nil {
		return Device{}, "", err
	}
	return d, tok, nil
}

func (s *Service) Authenticate(ctx context.Context, tok string) (Device, error) {
	if tok == "" {
		return Device{}, ErrUnauthorized
	}
	h := hash(tok)
	var d Device
	var stored []byte
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, platform, scopes, token_hash FROM devices
		 WHERE token_hash=$1 AND revoked_at IS NULL`, h,
	).Scan(&d.ID, &d.Name, &d.Platform, &d.Scopes, &stored)
	if err != nil {
		return Device{}, ErrUnauthorized
	}
	// The lookup already matched on hash; this guards against any future
	// change that widens the query.
	if subtle.ConstantTimeCompare(h, stored) != 1 {
		return Device{}, ErrUnauthorized
	}
	go func() {
		c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = s.pool.Exec(c, `UPDATE devices SET last_seen_at=now() WHERE id=$1`, d.ID)
	}()
	return d, nil
}

func (s *Service) Revoke(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `UPDATE devices SET revoked_at=now() WHERE id=$1`, id)
	return err
}

func (s *Service) List(ctx context.Context) ([]map[string]any, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id,name,platform,scopes,created_at,last_seen_at,revoked_at
		 FROM devices ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id uuid.UUID
		var name, platform string
		var scopes []string
		var created time.Time
		var seen, revoked *time.Time
		if err := rows.Scan(&id, &name, &platform, &scopes, &created, &seen, &revoked); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{
			"id": id, "name": name, "platform": platform, "scopes": scopes,
			"created_at": created, "last_seen_at": seen, "revoked_at": revoked,
		})
	}
	return out, rows.Err()
}

type ctxKey struct{}

func DeviceFrom(ctx context.Context) (Device, bool) {
	d, ok := ctx.Value(ctxKey{}).(Device)
	return d, ok
}

// Middleware rejects anything without a valid bearer token.
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		d, err := s.Authenticate(r.Context(), strings.TrimSpace(tok))
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, d)))
	})
}
