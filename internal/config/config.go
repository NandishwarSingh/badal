// Package config loads badal's runtime configuration from the environment.
package config

import (
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	Addr        string // http listen address
	DatabaseURL string

	S3Endpoint string
	S3Region   string
	S3Bucket   string
	S3KeyID    string
	S3Secret   string
	S3UseSSL   bool

	// SurrealDB holds the derived note graph. Optional: if unset the API runs
	// without projection, which keeps notes usable when Surreal is down.
	SurrealURL  string
	SurrealUser string
	SurrealPass string
	SurrealNS   string
	SurrealDB   string

	NtfyURL   string
	NtfyToken string

	// Chunking. FastCDC picks boundaries by content, so an edit near the
	// start of a file does not shift every later chunk.
	ChunkMin int
	ChunkAvg int
	ChunkMax int
}

func Load() (*Config, error) {
	c := &Config{
		Addr:        env("BADAL_ADDR", ":8080"),
		DatabaseURL: os.Getenv("BADAL_DATABASE_URL"),
		S3Endpoint:  env("BADAL_S3_ENDPOINT", "127.0.0.1:3900"),
		S3Region:    env("BADAL_S3_REGION", "badal"),
		S3Bucket:    env("BADAL_S3_BUCKET", "badal-hot"),
		S3KeyID:     os.Getenv("BADAL_S3_KEY_ID"),
		S3Secret:    os.Getenv("BADAL_S3_SECRET"),
		S3UseSSL:    env("BADAL_S3_SSL", "false") == "true",
		SurrealURL:  os.Getenv("BADAL_SURREAL_URL"),
		SurrealUser: env("BADAL_SURREAL_USER", "root"),
		SurrealPass: os.Getenv("BADAL_SURREAL_PASS"),
		SurrealNS:   env("BADAL_SURREAL_NS", "badal"),
		SurrealDB:   env("BADAL_SURREAL_DB", "notes"),
		NtfyURL:     os.Getenv("BADAL_NTFY_URL"),
		NtfyToken:   os.Getenv("BADAL_NTFY_TOKEN"),
		ChunkMin:    envInt("BADAL_CHUNK_MIN", 256*1024),
		ChunkAvg:    envInt("BADAL_CHUNK_AVG", 1024*1024),
		ChunkMax:    envInt("BADAL_CHUNK_MAX", 4*1024*1024),
	}
	if c.DatabaseURL == "" {
		return nil, fmt.Errorf("BADAL_DATABASE_URL is required")
	}
	if c.S3KeyID == "" || c.S3Secret == "" {
		return nil, fmt.Errorf("BADAL_S3_KEY_ID and BADAL_S3_SECRET are required")
	}
	return c, nil
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
