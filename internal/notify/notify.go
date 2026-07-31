// Package notify keeps the in-app record of things that happened.
//
// It sits beside Web Push rather than under it. A push is a delivery attempt:
// it can be dismissed, missed while the phone is face-down, blocked because
// permission was never granted, or silently dropped because the subscription
// expired months ago. None of those leave anything behind.
//
// Writing the row first and pushing second means the app always has the record
// even when the notification never arrives, which is also the only way you find
// out that push has stopped working.
package notify

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

type Notification struct {
	ID        uuid.UUID  `json:"id"`
	Kind      string     `json:"kind"`
	Title     string     `json:"title"`
	Body      string     `json:"body"`
	URL       string     `json:"url"`
	TaskID    *uuid.UUID `json:"task_id,omitempty"`
	EventID   *uuid.UUID `json:"event_id,omitempty"`
	ReadAt    *time.Time `json:"read_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

type Input struct {
	Kind    string
	Title   string
	Body    string
	URL     string
	TaskID  *uuid.UUID
	EventID *uuid.UUID
	// Optional. Two unread notifications with the same key collapse into one,
	// so a snoozed-then-refired reminder does not leave a trail of duplicates.
	DedupeKey string
}

func (s *Service) Add(ctx context.Context, in Input) error {
	if in.URL == "" {
		in.URL = "/"
	}
	var key any
	if in.DedupeKey != "" {
		key = in.DedupeKey
	}
	// The unique index only covers unread rows, so this refreshes an unread
	// duplicate and leaves read history alone.
	_, err := s.pool.Exec(ctx, `
		INSERT INTO notifications (kind, title, body, url, task_id, event_id, dedupe_key)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (dedupe_key) WHERE dedupe_key IS NOT NULL AND read_at IS NULL
		DO UPDATE SET title=EXCLUDED.title, body=EXCLUDED.body,
		              url=EXCLUDED.url, created_at=now()`,
		in.Kind, in.Title, in.Body, in.URL, in.TaskID, in.EventID, key)
	return err
}

func (s *Service) List(ctx context.Context, limit int) ([]Notification, int, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, kind, title, body, url, task_id, event_id, read_at, created_at
		  FROM notifications
		 ORDER BY read_at IS NOT NULL, created_at DESC
		 LIMIT $1`, limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Notification{}
	for rows.Next() {
		var n Notification
		if err := rows.Scan(&n.ID, &n.Kind, &n.Title, &n.Body, &n.URL,
			&n.TaskID, &n.EventID, &n.ReadAt, &n.CreatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var unread int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM notifications WHERE read_at IS NULL`).Scan(&unread); err != nil {
		return nil, 0, err
	}
	return out, unread, nil
}

func (s *Service) Unread(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM notifications WHERE read_at IS NULL`).Scan(&n)
	return n, err
}

// MarkRead marks one, or everything when id is nil.
func (s *Service) MarkRead(ctx context.Context, id *uuid.UUID) error {
	if id == nil {
		_, err := s.pool.Exec(ctx,
			`UPDATE notifications SET read_at=now() WHERE read_at IS NULL`)
		return err
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE notifications SET read_at=now() WHERE id=$1 AND read_at IS NULL`, *id)
	return err
}

func (s *Service) Clear(ctx context.Context) error {
	// Read ones only: clearing something you have not seen defeats the point.
	_, err := s.pool.Exec(ctx, `DELETE FROM notifications WHERE read_at IS NOT NULL`)
	return err
}

// Prune keeps the table from growing without bound. Read notifications older
// than a month are not history anyone consults.
func (s *Service) Prune(ctx context.Context) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM notifications WHERE read_at IS NOT NULL AND created_at < now() - interval '30 days'`)
	return err
}

// AddTask matches the narrow interface the reminder sweeper depends on, so
// tasks needs no import of this package.
func (s *Service) AddTask(ctx context.Context, kind, title, body, url string, taskID uuid.UUID, dedupe string) error {
	return s.Add(ctx, Input{
		Kind: kind, Title: title, Body: body, URL: url,
		TaskID: &taskID, DedupeKey: dedupe,
	})
}
