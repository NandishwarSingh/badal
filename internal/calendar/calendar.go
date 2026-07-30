// Package calendar serves the events extracted from mail.
//
// Events carry their provenance -- the message they came from and the verbatim
// span that justified the date -- so the UI can always answer "why is this in
// my calendar?". That question is the difference between a calendar you trust
// and one you ignore.
package calendar

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("event not found")

type Event struct {
	ID         uuid.UUID  `json:"id"`
	Title      string     `json:"title"`
	StartsAt   time.Time  `json:"starts_at"`
	EndsAt     *time.Time `json:"ends_at,omitempty"`
	AllDay     bool       `json:"all_day"`
	Location   string     `json:"location"`
	Kind       string     `json:"kind"`
	Notes      string     `json:"notes"`
	State      string     `json:"state"`
	Confidence float32    `json:"confidence"`
	Evidence   string     `json:"evidence"`
	// Provenance, joined in so the UI never needs a second request to explain
	// where an event came from.
	SourceID      *uuid.UUID `json:"source_message,omitempty"`
	SourceSubject string     `json:"source_subject,omitempty"`
	SourceFrom    string     `json:"source_from,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
}

type Service struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

const cols = `e.id, e.title, e.starts_at, e.ends_at, e.all_day, e.location, e.kind,
              e.notes, e.state, e.confidence, e.evidence, e.source_message,
              coalesce(m.subject,''), coalesce(nullif(m.from_name,''), coalesce(m.from_addr,'')),
              e.created_at`

func scan(rows pgx.Rows) ([]Event, error) {
	out := []Event{}
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.Title, &e.StartsAt, &e.EndsAt, &e.AllDay, &e.Location,
			&e.Kind, &e.Notes, &e.State, &e.Confidence, &e.Evidence, &e.SourceID,
			&e.SourceSubject, &e.SourceFrom, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Range returns events overlapping [from,to). Rejected ones are excluded --
// saying no should mean it disappears.
func (s *Service) Range(ctx context.Context, from, to time.Time, includeProposed bool) ([]Event, error) {
	q := `SELECT ` + cols + `
	        FROM events e LEFT JOIN messages m ON m.id = e.source_message
	       WHERE e.deleted_at IS NULL AND e.state <> 'rejected'
	         AND e.starts_at >= $1 AND e.starts_at < $2`
	if !includeProposed {
		q += ` AND e.state = 'confirmed'`
	}
	q += ` ORDER BY e.starts_at`
	rows, err := s.pool.Query(ctx, q, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scan(rows)
}

// Proposed is the review queue.
func (s *Service) Proposed(ctx context.Context, limit int) ([]Event, error) {
	if limit <= 0 || limit > 300 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+cols+`
		   FROM events e LEFT JOIN messages m ON m.id = e.source_message
		  WHERE e.deleted_at IS NULL AND e.state = 'proposed'
		  ORDER BY e.starts_at LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scan(rows)
}

func (s *Service) Counts(ctx context.Context) (map[string]int, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT state, count(*) FROM events WHERE deleted_at IS NULL GROUP BY state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{"proposed": 0, "confirmed": 0, "rejected": 0}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return nil, err
		}
		out[k] = n
	}
	return out, rows.Err()
}

func (s *Service) SetState(ctx context.Context, id uuid.UUID, state string) (Event, error) {
	switch state {
	case "proposed", "confirmed", "rejected":
	default:
		return Event{}, fmt.Errorf("unknown state %q", state)
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE events SET state=$2, updated_at=now() WHERE id=$1 AND deleted_at IS NULL`, id, state)
	if err != nil {
		return Event{}, err
	}
	return s.Get(ctx, id)
}

// ConfirmAll accepts everything currently pending. Offered because reviewing
// 40 backfilled events one at a time is how a review queue gets abandoned.
func (s *Service) ConfirmAll(ctx context.Context, minConf float64) (int, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE events SET state='confirmed', updated_at=now()
		  WHERE state='proposed' AND deleted_at IS NULL AND confidence >= $1`, minConf)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

type Patch struct {
	Title    *string    `json:"title"`
	StartsAt *time.Time `json:"starts_at"`
	EndsAt   *time.Time `json:"ends_at"`
	AllDay   *bool      `json:"all_day"`
	Location *string    `json:"location"`
	Kind     *string    `json:"kind"`
	Notes    *string    `json:"notes"`
}

// Update lets the user correct a wrong extraction in place, which is usually
// faster than rejecting it and typing the event out again.
func (s *Service) Update(ctx context.Context, id uuid.UUID, p Patch) (Event, error) {
	_, err := s.pool.Exec(ctx,
		`UPDATE events SET
		   title     = coalesce($2, title),
		   starts_at = coalesce($3, starts_at),
		   ends_at   = coalesce($4, ends_at),
		   all_day   = coalesce($5, all_day),
		   location  = coalesce($6, location),
		   kind      = coalesce($7, kind),
		   notes     = coalesce($8, notes),
		   updated_at = now()
		 WHERE id=$1 AND deleted_at IS NULL`,
		id, p.Title, p.StartsAt, p.EndsAt, p.AllDay, p.Location, p.Kind, p.Notes)
	if err != nil {
		return Event{}, err
	}
	return s.Get(ctx, id)
}

// Create is the manual path: not everything worth scheduling arrives by email.
func (s *Service) Create(ctx context.Context, e Event) (Event, error) {
	if e.Kind == "" {
		e.Kind = "event"
	}
	var id uuid.UUID
	err := s.pool.QueryRow(ctx,
		`INSERT INTO events (title, starts_at, ends_at, all_day, location, kind, notes,
		                     state, confidence, evidence)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,'confirmed',1,'') RETURNING id`,
		e.Title, e.StartsAt, e.EndsAt, e.AllDay, e.Location, e.Kind, e.Notes).Scan(&id)
	if err != nil {
		return Event{}, err
	}
	return s.Get(ctx, id)
}

func (s *Service) Get(ctx context.Context, id uuid.UUID) (Event, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+cols+` FROM events e LEFT JOIN messages m ON m.id = e.source_message
		  WHERE e.id=$1`, id)
	if err != nil {
		return Event{}, err
	}
	defer rows.Close()
	list, err := scan(rows)
	if err != nil {
		return Event{}, err
	}
	if len(list) == 0 {
		return Event{}, ErrNotFound
	}
	return list[0], nil
}

func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE events SET deleted_at=now() WHERE id=$1 AND deleted_at IS NULL`, id)
	return err
}
