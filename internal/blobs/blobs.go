// Package blobs implements content-addressed storage over chunks + Postgres.
//
// Upload is probe-then-send: the client hashes locally, asks which chunks the
// server lacks, and uploads only those. A re-upload of a barely-changed file
// therefore moves one chunk, not the file.
package blobs

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NandishwarSingh/badal/internal/chunks"
	"github.com/NandishwarSingh/badal/internal/objects"
	"github.com/NandishwarSingh/badal/internal/syncfeed"
)

var (
	ErrNotFound     = errors.New("not found")
	ErrMissingChunk = errors.New("missing chunks")
	ErrEvicted      = errors.New("evicted to archive")
)

type Service struct {
	pool *pgxpool.Pool
	obj  *objects.Store
	feed *syncfeed.Feed
}

func New(pool *pgxpool.Pool, obj *objects.Store, feed *syncfeed.Feed) *Service {
	return &Service{pool: pool, obj: obj, feed: feed}
}

type File struct {
	ID          uuid.UUID `json:"id"`
	Path        string    `json:"path"`
	Size        int64     `json:"size"`
	Hash        string    `json:"hash"`
	ContentType string    `json:"content_type"`
	Version     int       `json:"version"`
	HLC         string    `json:"hlc"`
}

// Probe returns the subset of hashes the server does not already hold.
func (s *Service) Probe(ctx context.Context, hashes [][]byte) ([][]byte, error) {
	if len(hashes) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT hash FROM chunks WHERE hash = ANY($1)`, hashes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	have := map[string]bool{}
	for rows.Next() {
		var h []byte
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		have[string(h)] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	missing := [][]byte{}
	seen := map[string]bool{}
	for _, h := range hashes {
		if have[string(h)] || seen[string(h)] {
			continue
		}
		seen[string(h)] = true
		missing = append(missing, h)
	}
	return missing, nil
}

// PutChunk stores one chunk. The hash is recomputed from the body rather than
// trusted -- otherwise a client could poison an address with wrong content and
// corrupt every file that later dedupes against it.
func (s *Service) PutChunk(ctx context.Context, hash, data []byte) error {
	if !chunks.Verify(hash, data) {
		return fmt.Errorf("chunk content does not match its hash")
	}
	if err := s.obj.Put(ctx, chunks.Key(hash), data); err != nil {
		return fmt.Errorf("object put: %w", err)
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO chunks (hash, size) VALUES ($1,$2) ON CONFLICT (hash) DO NOTHING`,
		hash, len(data))
	return err
}

// Commit creates (or updates) a file from an ordered chunk list. Blob, file,
// refcounts and the change-feed row all land in one transaction.
func (s *Service) Commit(ctx context.Context, device uuid.UUID, path string, wholeHash []byte,
	size int64, chunkHashes [][]byte, contentType string) (File, error) {

	// Refuse rather than create a file with holes in it.
	missing, err := s.Probe(ctx, chunkHashes)
	if err != nil {
		return File{}, err
	}
	if len(missing) > 0 {
		return File{}, fmt.Errorf("%w: %d chunk(s)", ErrMissingChunk, len(missing))
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return File{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var blobID uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM blobs WHERE hash=$1`, wholeHash).Scan(&blobID)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := tx.QueryRow(ctx,
			`INSERT INTO blobs (hash, size, chunk_hashes, content_type)
			 VALUES ($1,$2,$3,$4) RETURNING id`,
			wholeHash, size, chunkHashes, contentType).Scan(&blobID); err != nil {
			return File{}, fmt.Errorf("insert blob: %w", err)
		}
		// One reference per distinct chunk in this blob.
		uniq := map[string]bool{}
		for _, h := range chunkHashes {
			if uniq[string(h)] {
				continue
			}
			uniq[string(h)] = true
			if _, err := tx.Exec(ctx,
				`UPDATE chunks SET refcount = refcount + 1 WHERE hash=$1`, h); err != nil {
				return File{}, err
			}
		}
	} else if err != nil {
		return File{}, err
	}

	hlc := s.feed.Clock.Now()
	var f File
	f.ContentType = contentType
	err = tx.QueryRow(ctx,
		`INSERT INTO files (path, blob_id, size, hlc)
		 VALUES ($1,$2,$3,$4)
		 ON CONFLICT (path) WHERE deleted_at IS NULL
		 DO UPDATE SET blob_id=EXCLUDED.blob_id, size=EXCLUDED.size,
		               version=files.version+1, hlc=EXCLUDED.hlc, updated_at=now()
		 RETURNING id, path, size, version, hlc`,
		path, blobID, size, hlc).Scan(&f.ID, &f.Path, &f.Size, &f.Version, &f.HLC)
	if err != nil {
		return File{}, fmt.Errorf("upsert file: %w", err)
	}
	if _, err := s.feed.Append(ctx, tx, "file", f.ID, "upsert", device); err != nil {
		return File{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return File{}, err
	}
	f.Hash = chunks.Hex(wholeHash)
	return f, nil
}

// Open streams a file's content by concatenating its chunks in order.
func (s *Service) Open(ctx context.Context, path string, w io.Writer) (File, error) {
	var f File
	var blobHash []byte
	var chunkHashes [][]byte
	var evicted *string
	err := s.pool.QueryRow(ctx,
		`SELECT f.id, f.path, f.size, f.version, f.hlc,
		        b.hash, b.chunk_hashes, b.content_type, b.evicted_at::text
		 FROM files f JOIN blobs b ON b.id = f.blob_id
		 WHERE f.path=$1 AND f.deleted_at IS NULL`, path,
	).Scan(&f.ID, &f.Path, &f.Size, &f.Version, &f.HLC,
		&blobHash, &chunkHashes, &f.ContentType, &evicted)
	if errors.Is(err, pgx.ErrNoRows) {
		return File{}, ErrNotFound
	}
	if err != nil {
		return File{}, err
	}
	f.Hash = chunks.Hex(blobHash)
	if evicted != nil {
		// Content lives only in the archive; the caller turns this into a
		// 202 plus a queued retrieval rather than an error.
		return f, ErrEvicted
	}
	if w == nil {
		return f, nil
	}
	for i, h := range chunkHashes {
		data, err := s.obj.Get(ctx, chunks.Key(h))
		if err != nil {
			return f, fmt.Errorf("chunk %d/%d: %w", i+1, len(chunkHashes), err)
		}
		if !chunks.Verify(h, data) {
			return f, fmt.Errorf("chunk %d/%d failed integrity check", i+1, len(chunkHashes))
		}
		if _, err := w.Write(data); err != nil {
			return f, err
		}
	}
	return f, nil
}

func (s *Service) List(ctx context.Context, prefix string) ([]File, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT f.id, f.path, f.size, f.version, f.hlc, b.hash, b.content_type
		 FROM files f JOIN blobs b ON b.id=f.blob_id
		 WHERE f.deleted_at IS NULL AND f.path LIKE $1 ORDER BY f.path`, prefix+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []File{}
	for rows.Next() {
		var f File
		var h []byte
		if err := rows.Scan(&f.ID, &f.Path, &f.Size, &f.Version, &f.HLC, &h, &f.ContentType); err != nil {
			return nil, err
		}
		f.Hash = chunks.Hex(h)
		out = append(out, f)
	}
	return out, rows.Err()
}

// Delete soft-deletes so the tombstone can propagate through the change feed.
func (s *Service) Delete(ctx context.Context, device uuid.UUID, path string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var id uuid.UUID
	err = tx.QueryRow(ctx,
		`UPDATE files SET deleted_at=now() WHERE path=$1 AND deleted_at IS NULL RETURNING id`,
		path).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if _, err := s.feed.Append(ctx, tx, "file", id, "delete", device); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
