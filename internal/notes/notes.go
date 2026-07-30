// Package notes owns the note graph's source of truth in Postgres.
//
// Writes are version-checked. When two devices edit the same note offline, the
// loser becomes a conflict copy rather than being discarded -- you can read
// both and merge by hand, which is more trustworthy than a silent merge.
package notes

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NandishwarSingh/badal/internal/syncfeed"
)

var (
	ErrNotFound = errors.New("note not found")
	ErrConflict = errors.New("version conflict")
)

type Note struct {
	ID         uuid.UUID  `json:"id"`
	Slug       string     `json:"slug"`
	Title      string     `json:"title"`
	Body       string     `json:"body"`
	Tags       []string   `json:"tags"`
	Version    int        `json:"version"`
	HLC        string     `json:"hlc"`
	ConflictOf *uuid.UUID `json:"conflict_of,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

type Link struct {
	Slug  string     `json:"slug"`
	Title string     `json:"title,omitempty"`
	ID    *uuid.UUID `json:"id,omitempty"` // nil when the target does not exist yet
}

type Service struct {
	pool *pgxpool.Pool
	feed *syncfeed.Feed
}

func New(pool *pgxpool.Pool, feed *syncfeed.Feed) *Service {
	return &Service{pool: pool, feed: feed}
}

// ---------------------------------------------------------------- slugs

var (
	wikiLink   = regexp.MustCompile(`\[\[([^\]|]+)(?:\|[^\]]*)?\]\]`)
	dashRuns   = regexp.MustCompile(`-+`)
	slugStripR = regexp.MustCompile(`^-|-$`)
)

// Slugify produces the stable key a [[wiki-link]] resolves against.
func Slugify(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := slugStripR.ReplaceAllString(dashRuns.ReplaceAllString(b.String(), "-"), "")
	if out == "" {
		out = "note"
	}
	if len(out) > 120 {
		out = strings.TrimSuffix(out[:120], "-")
	}
	return out
}

// ParseLinks extracts [[targets]] from a body, de-duplicated, in order.
// [[Target|display text]] resolves on the target.
func ParseLinks(body string) []string {
	m := wikiLink.FindAllStringSubmatch(body, -1)
	seen := map[string]bool{}
	out := []string{}
	for _, g := range m {
		s := Slugify(g[1])
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// ---------------------------------------------------------------- writes

type PutInput struct {
	Slug    string
	Title   string
	Body    string
	Tags    []string
	Version *int // nil = create or blind write; set = optimistic concurrency
}

// Put creates or updates a note. If Version is supplied and does not match the
// stored row, the incoming edit is written as a conflict copy and ErrConflict
// is returned alongside it -- nothing is ever discarded.
func (s *Service) Put(ctx context.Context, device uuid.UUID, in PutInput) (Note, error) {
	if strings.TrimSpace(in.Title) == "" {
		return Note{}, fmt.Errorf("title is required")
	}
	if in.Slug == "" {
		in.Slug = Slugify(in.Title)
	}
	if in.Tags == nil {
		in.Tags = []string{}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Note{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Lock the live row so a concurrent writer cannot slip between the version
	// check and the update.
	var curID uuid.UUID
	var curVersion int
	err = tx.QueryRow(ctx,
		`SELECT id, version FROM notes WHERE slug=$1 AND deleted_at IS NULL FOR UPDATE`,
		in.Slug).Scan(&curID, &curVersion)
	exists := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Note{}, err
	}

	if exists && in.Version != nil && *in.Version != curVersion {
		n, cerr := s.writeConflictCopy(ctx, tx, device, in, curID, curVersion)
		if cerr != nil {
			return Note{}, cerr
		}
		if err := tx.Commit(ctx); err != nil {
			return Note{}, err
		}
		return n, ErrConflict
	}

	hlc := s.feed.Clock.Now()
	var n Note
	if exists {
		err = tx.QueryRow(ctx,
			`UPDATE notes SET title=$2, body=$3, tags=$4, version=version+1,
			        hlc=$5, updated_at=now()
			 WHERE id=$1
			 RETURNING id, slug, title, body, tags, version, hlc, conflict_of, created_at, updated_at`,
			curID, in.Title, in.Body, in.Tags, hlc,
		).Scan(&n.ID, &n.Slug, &n.Title, &n.Body, &n.Tags, &n.Version, &n.HLC, &n.ConflictOf, &n.CreatedAt, &n.UpdatedAt)
	} else {
		err = tx.QueryRow(ctx,
			`INSERT INTO notes (slug, title, body, tags, hlc)
			 VALUES ($1,$2,$3,$4,$5)
			 RETURNING id, slug, title, body, tags, version, hlc, conflict_of, created_at, updated_at`,
			in.Slug, in.Title, in.Body, in.Tags, hlc,
		).Scan(&n.ID, &n.Slug, &n.Title, &n.Body, &n.Tags, &n.Version, &n.HLC, &n.ConflictOf, &n.CreatedAt, &n.UpdatedAt)
	}
	if err != nil {
		return Note{}, fmt.Errorf("write note: %w", err)
	}

	if err := s.syncLinks(ctx, tx, n.ID, in.Body); err != nil {
		return Note{}, err
	}
	if _, err := s.feed.Append(ctx, tx, "note", n.ID, "upsert", device); err != nil {
		return Note{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Note{}, err
	}
	return n, nil
}

func (s *Service) writeConflictCopy(ctx context.Context, tx pgx.Tx, device uuid.UUID,
	in PutInput, origID uuid.UUID, origVersion int) (Note, error) {

	stamp := time.Now().UTC().Format("20060102-150405")
	hlc := s.feed.Clock.Now()
	var n Note
	err := tx.QueryRow(ctx,
		`INSERT INTO notes (slug, title, body, tags, hlc, conflict_of)
		 VALUES ($1,$2,$3,$4,$5,$6)
		 RETURNING id, slug, title, body, tags, version, hlc, conflict_of, created_at, updated_at`,
		fmt.Sprintf("%s-conflict-%s", in.Slug, stamp),
		fmt.Sprintf("%s (conflict %s)", in.Title, stamp),
		in.Body, in.Tags, hlc, origID,
	).Scan(&n.ID, &n.Slug, &n.Title, &n.Body, &n.Tags, &n.Version, &n.HLC, &n.ConflictOf, &n.CreatedAt, &n.UpdatedAt)
	if err != nil {
		return Note{}, fmt.Errorf("conflict copy: %w", err)
	}
	if err := s.syncLinks(ctx, tx, n.ID, in.Body); err != nil {
		return Note{}, err
	}
	if _, err := s.feed.Append(ctx, tx, "note", n.ID, "upsert", device); err != nil {
		return Note{}, err
	}
	return n, nil
}

// syncLinks rewrites this note's outgoing edges and resolves any dangling
// links elsewhere that were waiting for this note's slug to exist.
func (s *Service) syncLinks(ctx context.Context, tx pgx.Tx, id uuid.UUID, body string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM note_links WHERE src_id=$1`, id); err != nil {
		return err
	}
	for _, slug := range ParseLinks(body) {
		if _, err := tx.Exec(ctx,
			`INSERT INTO note_links (src_id, dst_slug, dst_id)
			 VALUES ($1, $2, (SELECT id FROM notes WHERE slug=$2 AND deleted_at IS NULL))
			 ON CONFLICT (src_id, dst_slug) DO NOTHING`, id, slug); err != nil {
			return err
		}
	}
	// A newly created note may satisfy links written before it existed.
	_, err := tx.Exec(ctx,
		`UPDATE note_links SET dst_id=$1
		 WHERE dst_id IS NULL
		   AND dst_slug=(SELECT slug FROM notes WHERE id=$1)`, id)
	return err
}

func (s *Service) Delete(ctx context.Context, device uuid.UUID, slug string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var id uuid.UUID
	err = tx.QueryRow(ctx,
		`UPDATE notes SET deleted_at=now(), updated_at=now()
		 WHERE slug=$1 AND deleted_at IS NULL RETURNING id`, slug).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	// Inbound links survive as dangling -- the text still says [[slug]], and
	// re-creating the note should re-resolve them.
	if _, err := tx.Exec(ctx, `UPDATE note_links SET dst_id=NULL WHERE dst_id=$1`, id); err != nil {
		return err
	}
	if _, err := s.feed.Append(ctx, tx, "note", id, "delete", device); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ---------------------------------------------------------------- reads

const noteCols = `id, slug, title, body, tags, version, hlc, conflict_of, created_at, updated_at`

func scanNote(row pgx.Row) (Note, error) {
	var n Note
	err := row.Scan(&n.ID, &n.Slug, &n.Title, &n.Body, &n.Tags, &n.Version, &n.HLC,
		&n.ConflictOf, &n.CreatedAt, &n.UpdatedAt)
	return n, err
}

func (s *Service) Get(ctx context.Context, slug string) (Note, error) {
	n, err := scanNote(s.pool.QueryRow(ctx,
		`SELECT `+noteCols+` FROM notes WHERE slug=$1 AND deleted_at IS NULL`, slug))
	if errors.Is(err, pgx.ErrNoRows) {
		return Note{}, ErrNotFound
	}
	return n, err
}

// GetByID excludes soft-deleted rows on purpose: the projector treats
// "not found" and "deleted" identically, so a tombstone needs no special case.
func (s *Service) GetByID(ctx context.Context, id uuid.UUID) (Note, error) {
	n, err := scanNote(s.pool.QueryRow(ctx,
		`SELECT `+noteCols+` FROM notes WHERE id=$1 AND deleted_at IS NULL`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Note{}, ErrNotFound
	}
	return n, err
}

type ListOpts struct {
	Tag   string
	Limit int
}

func (s *Service) List(ctx context.Context, o ListOpts) ([]Note, error) {
	if o.Limit <= 0 || o.Limit > 500 {
		o.Limit = 100
	}
	q := `SELECT ` + noteCols + ` FROM notes WHERE deleted_at IS NULL`
	args := []any{}
	if o.Tag != "" {
		args = append(args, o.Tag)
		q += ` AND $1 = ANY(tags)`
	}
	args = append(args, o.Limit)
	q += fmt.Sprintf(` ORDER BY updated_at DESC LIMIT $%d`, len(args))

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Note{}
	for rows.Next() {
		n, err := scanNote(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// Backlinks lists notes that link TO this slug. This is the payoff of storing
// links as rows instead of only rendering them.
func (s *Service) Backlinks(ctx context.Context, slug string) ([]Link, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT n.id, n.slug, n.title
		   FROM note_links l JOIN notes n ON n.id = l.src_id
		  WHERE l.dst_slug=$1 AND n.deleted_at IS NULL
		  ORDER BY n.title`, slug)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Link{}
	for rows.Next() {
		var l Link
		var id uuid.UUID
		if err := rows.Scan(&id, &l.Slug, &l.Title); err != nil {
			return nil, err
		}
		l.ID = &id
		out = append(out, l)
	}
	return out, rows.Err()
}

// Outlinks lists targets this note points at, including ones not yet created.
func (s *Service) Outlinks(ctx context.Context, id uuid.UUID) ([]Link, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT l.dst_slug, l.dst_id, coalesce(n.title,'')
		   FROM note_links l LEFT JOIN notes n ON n.id = l.dst_id AND n.deleted_at IS NULL
		  WHERE l.src_id=$1 ORDER BY l.dst_slug`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Link{}
	for rows.Next() {
		var l Link
		if err := rows.Scan(&l.Slug, &l.ID, &l.Title); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

type Hit struct {
	Note    Note    `json:"note"`
	Rank    float32 `json:"rank"`
	Snippet string  `json:"snippet"`
}

// Search is full-text for now. Phase 5 blends this with vector similarity and
// graph proximity; keeping the shape stable means that is additive.
func (s *Service) Search(ctx context.Context, q string, limit int) ([]Hit, error) {
	if strings.TrimSpace(q) == "" {
		return []Hit{}, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+noteCols+`,
		        ts_rank(search, websearch_to_tsquery('english', $1)) AS rank,
		        ts_headline('english', body, websearch_to_tsquery('english', $1),
		                    'MaxWords=28, MinWords=8, ShortWord=3, MaxFragments=1')
		   FROM notes
		  WHERE deleted_at IS NULL AND search @@ websearch_to_tsquery('english', $1)
		  ORDER BY rank DESC, updated_at DESC LIMIT $2`, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Hit{}
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.Note.ID, &h.Note.Slug, &h.Note.Title, &h.Note.Body, &h.Note.Tags,
			&h.Note.Version, &h.Note.HLC, &h.Note.ConflictOf, &h.Note.CreatedAt, &h.Note.UpdatedAt,
			&h.Rank, &h.Snippet); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// Conflicts lists unresolved conflict copies so the UI can surface them.
func (s *Service) Conflicts(ctx context.Context) ([]Note, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+noteCols+` FROM notes
		  WHERE conflict_of IS NOT NULL AND deleted_at IS NULL
		  ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Note{}
	for rows.Next() {
		n, err := scanNote(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
