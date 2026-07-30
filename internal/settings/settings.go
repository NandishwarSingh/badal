// Package settings is a small typed view over the app_settings table.
//
// These live in Postgres rather than the environment so the PWA can change
// them without a redeploy -- auto-confirm in particular is a toggle the user
// is expected to flip, not a deployment concern.
package settings

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) All(ctx context.Context) (map[string]any, error) {
	rows, err := s.pool.Query(ctx, `SELECT key, value FROM app_settings ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]any{}
	for rows.Next() {
		var k string
		var raw []byte
		if err := rows.Scan(&k, &raw); err != nil {
			return nil, err
		}
		var v any
		_ = json.Unmarshal(raw, &v)
		out[k] = v
	}
	return out, rows.Err()
}

func (s *Store) Set(ctx context.Context, k string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO app_settings (key, value, updated_at) VALUES ($1,$2,now())
		 ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value, updated_at=now()`, k, b)
	return err
}

// The typed getters fall back to the supplied default rather than returning an
// error: a missing or malformed setting should never stop mail from syncing.

func (s *Store) Bool(ctx context.Context, k string, def bool) bool {
	var raw []byte
	if err := s.pool.QueryRow(ctx, `SELECT value FROM app_settings WHERE key=$1`, k).Scan(&raw); err != nil {
		return def
	}
	var v bool
	if json.Unmarshal(raw, &v) != nil {
		return def
	}
	return v
}

func (s *Store) Int(ctx context.Context, k string, def int) int {
	var raw []byte
	if err := s.pool.QueryRow(ctx, `SELECT value FROM app_settings WHERE key=$1`, k).Scan(&raw); err != nil {
		return def
	}
	var v float64
	if json.Unmarshal(raw, &v) != nil {
		return def
	}
	return int(v)
}

func (s *Store) Float(ctx context.Context, k string, def float64) float64 {
	var raw []byte
	if err := s.pool.QueryRow(ctx, `SELECT value FROM app_settings WHERE key=$1`, k).Scan(&raw); err != nil {
		return def
	}
	var v float64
	if json.Unmarshal(raw, &v) != nil {
		return def
	}
	return v
}
