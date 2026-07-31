// Package tasks owns the to-do list and its reminders.
//
// Writes are version-checked the same way notes are, but the losing edit is
// not parked as a conflict. A task is a short, mostly structured record --
// title, a due date, done or not -- and there is nothing to merge by hand the
// way there is with two paragraphs of prose. Last write wins, and the version
// check exists to catch a stale client rather than to preserve both sides.
package tasks

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NandishwarSingh/badal/internal/syncfeed"
)

var (
	ErrNotFound = errors.New("task not found")
	ErrConflict = errors.New("version conflict")
)

type Task struct {
	ID            uuid.UUID  `json:"id"`
	Title         string     `json:"title"`
	Notes         string     `json:"notes"`
	State         string     `json:"state"`
	Priority      int        `json:"priority"`
	DueAt         *time.Time `json:"due_at,omitempty"`
	AllDay        bool       `json:"all_day"`
	NoteID        *uuid.UUID `json:"note_id,omitempty"`
	EventID       *uuid.UUID `json:"event_id,omitempty"`
	MessageID     *uuid.UUID `json:"message_id,omitempty"`
	RecurEvery    string     `json:"recur_every,omitempty"`
	RecurInterval int        `json:"recur_interval,omitempty"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
	Version       int        `json:"version"`
	HLC           string     `json:"hlc"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	Reminders     []Reminder `json:"reminders"`
	Subjects      []string   `json:"subjects,omitempty"`
}

type Reminder struct {
	ID      uuid.UUID  `json:"id"`
	FireAt  time.Time  `json:"fire_at"`
	FiredAt *time.Time `json:"fired_at,omitempty"`
}

type Service struct {
	pool *pgxpool.Pool
	feed *syncfeed.Feed
}

func New(pool *pgxpool.Pool, feed *syncfeed.Feed) *Service {
	return &Service{pool: pool, feed: feed}
}

// ------------------------------------------------------------------ writes

type PutInput struct {
	ID        *uuid.UUID  `json:"id"`
	Title     string      `json:"title"`
	Notes     string      `json:"notes"`
	State     string      `json:"state"`
	Priority  int         `json:"priority"`
	DueAt     *time.Time  `json:"due_at"`
	AllDay    *bool       `json:"all_day"`
	NoteID    *uuid.UUID  `json:"note_id"`
	EventID   *uuid.UUID  `json:"event_id"`
	MessageID *uuid.UUID  `json:"message_id"`
	Version   *int        `json:"version"`
	RemindAt  []time.Time `json:"remind_at"`

	// "" or absent means it does not repeat. Interval defaults to 1.
	RecurEvery    string `json:"recur_every"`
	RecurInterval int    `json:"recur_interval"`
}

// Put creates or updates a task, replacing its reminders.
func (s *Service) Put(ctx context.Context, device uuid.UUID, in PutInput) (Task, error) {
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return Task{}, fmt.Errorf("title is required")
	}
	state := in.State
	if state == "" {
		state = "open"
	}
	if state != "open" && state != "done" && state != "dropped" {
		return Task{}, fmt.Errorf("bad state %q", state)
	}
	allDay := true
	if in.AllDay != nil {
		allDay = *in.AllDay
	}
	if in.Priority < 0 || in.Priority > 3 {
		return Task{}, fmt.Errorf("priority must be 0..3")
	}
	switch in.RecurEvery {
	case "", "day", "week", "month", "year":
	default:
		return Task{}, fmt.Errorf("bad recur_every %q", in.RecurEvery)
	}
	// A repeat needs something to advance from, and the constraint would
	// reject it anyway -- better a clear message than a constraint violation.
	if in.RecurEvery != "" && in.DueAt == nil {
		return Task{}, fmt.Errorf("a repeating task needs a due date")
	}
	interval := in.RecurInterval
	if interval < 1 {
		interval = 1
	}
	var every any
	if in.RecurEvery != "" {
		every = in.RecurEvery
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Task{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	hlc := s.feed.Clock.Now()
	var id uuid.UUID

	if in.ID == nil {
		err = tx.QueryRow(ctx, `
			INSERT INTO tasks (title, notes, state, priority, due_at, all_day,
			                   note_id, event_id, message_id, hlc,
			                   recur_every, recur_interval, completed_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,
			        CASE WHEN $3='done' THEN now() END)
			RETURNING id`,
			title, in.Notes, state, in.Priority, in.DueAt, allDay,
			in.NoteID, in.EventID, in.MessageID, hlc, every, interval).Scan(&id)
		if err != nil {
			return Task{}, err
		}
	} else {
		id = *in.ID
		var current int
		err = tx.QueryRow(ctx,
			`SELECT version FROM tasks WHERE id=$1 AND deleted_at IS NULL`, id).Scan(&current)
		if errors.Is(err, pgx.ErrNoRows) {
			// A task written offline already has the id the client gave it, so
			// an id with no row is an insert arriving late, not a miss. Failing
			// here would strand everything captured in aeroplane mode.
			if _, err := tx.Exec(ctx, `
				INSERT INTO tasks (id, title, notes, state, priority, due_at, all_day,
				                   note_id, event_id, message_id, hlc,
				                   recur_every, recur_interval, completed_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,
				        CASE WHEN $4='done' THEN now() END)
				ON CONFLICT (id) DO NOTHING`,
				id, title, in.Notes, state, in.Priority, in.DueAt, allDay,
				in.NoteID, in.EventID, in.MessageID, hlc, every, interval); err != nil {
				return Task{}, err
			}
			current = -1 // skip the update below
		} else if err != nil {
			return Task{}, err
		}
		if current >= 0 {
			// A stale client is told to re-read rather than allowed to
			// overwrite a change it never saw.
			if in.Version != nil && *in.Version != current {
				return Task{}, ErrConflict
			}
			_, err = tx.Exec(ctx, `
			UPDATE tasks
			   SET title=$2, notes=$3, state=$4, priority=$5, due_at=$6, all_day=$7,
			       note_id=$8, event_id=$9, message_id=$10,
			       -- Stamped when it first becomes done, cleared if reopened, and
			       -- left alone on an edit that does not change the state.
			       completed_at = CASE
			           WHEN $4='done' AND completed_at IS NULL THEN now()
			           WHEN $4<>'done' THEN NULL
			           ELSE completed_at END,
			       recur_every=$12, recur_interval=$13,
			       version=version+1, hlc=$11, updated_at=now()
			 WHERE id=$1`,
				id, title, in.Notes, state, in.Priority, in.DueAt, allDay,
				in.NoteID, in.EventID, in.MessageID, hlc, every, interval)
			if err != nil {
				return Task{}, err
			}
		}
	}

	// Reminders are replaced wholesale. Diffing them against what is there
	// would be more code for no gain: there are rarely more than two, and a
	// reminder that has already fired is recreated as unfired only if the
	// caller actually re-sends that time.
	if _, err := tx.Exec(ctx, `DELETE FROM task_reminders WHERE task_id=$1`, id); err != nil {
		return Task{}, err
	}
	for _, t := range in.RemindAt {
		if t.IsZero() {
			continue
		}
		// A reminder in the past would fire immediately on the next sweep,
		// which is startling rather than useful.
		fired := any(nil)
		if t.Before(time.Now()) {
			fired = time.Now()
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO task_reminders (task_id, fire_at, fired_at) VALUES ($1,$2,$3)`,
			id, t, fired); err != nil {
			return Task{}, err
		}
	}

	if _, err := s.feed.Append(ctx, tx, "task", id, "upsert", device); err != nil {
		return Task{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Task{}, err
	}
	return s.Get(ctx, id)
}

// SetState is the one-tap path: tick, untick, or drop. Completing a repeating
// task also schedules the next occurrence, which is why it needs a timezone --
// "every month" has to land at the same local hour after a DST shift.
func (s *Service) SetState(ctx context.Context, device uuid.UUID, id uuid.UUID, state string, loc *time.Location) (Task, error) {
	if state != "open" && state != "done" && state != "dropped" {
		return Task{}, fmt.Errorf("bad state %q", state)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Task{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `
		UPDATE tasks
		   SET state=$2,
		       completed_at = CASE WHEN $2='done' THEN coalesce(completed_at, now()) END,
		       version=version+1, hlc=$3, updated_at=now()
		 WHERE id=$1 AND deleted_at IS NULL`, id, state, s.feed.Clock.Now())
	if err != nil {
		return Task{}, err
	}
	if tag.RowsAffected() == 0 {
		return Task{}, ErrNotFound
	}
	// Finishing a task retires whatever was still going to nag about it.
	if state != "open" {
		if _, err := tx.Exec(ctx,
			`UPDATE task_reminders SET fired_at=now() WHERE task_id=$1 AND fired_at IS NULL`,
			id); err != nil {
			return Task{}, err
		}
	}
	// A repeat closes and reappears rather than moving, so the Done list is a
	// record of what you actually finished.
	if state == "done" {
		if loc == nil {
			loc = time.UTC
		}
		nextID, err := s.spawnNext(ctx, tx, id, loc)
		if err != nil {
			return Task{}, err
		}
		if nextID != uuid.Nil {
			if _, err := s.feed.Append(ctx, tx, "task", nextID, "upsert", device); err != nil {
				return Task{}, err
			}
		}
	}
	if _, err := s.feed.Append(ctx, tx, "task", id, "upsert", device); err != nil {
		return Task{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Task{}, err
	}
	return s.Get(ctx, id)
}

// Snooze pushes every unfired reminder out, and creates one if the task had
// none left -- which is the case when snoozing from a notification.
func (s *Service) Snooze(ctx context.Context, device, id uuid.UUID, d time.Duration) (Task, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Task{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	at := time.Now().Add(d)
	tag, err := tx.Exec(ctx,
		`UPDATE task_reminders SET fire_at=$2, fired_at=NULL WHERE task_id=$1 AND fired_at IS NULL`,
		id, at)
	if err != nil {
		return Task{}, err
	}
	if tag.RowsAffected() == 0 {
		var exists bool
		if err := tx.QueryRow(ctx,
			`SELECT true FROM tasks WHERE id=$1 AND deleted_at IS NULL`, id).Scan(&exists); err != nil {
			return Task{}, ErrNotFound
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO task_reminders (task_id, fire_at) VALUES ($1,$2)`, id, at); err != nil {
			return Task{}, err
		}
	}
	if _, err := s.feed.Append(ctx, tx, "task", id, "upsert", device); err != nil {
		return Task{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Task{}, err
	}
	return s.Get(ctx, id)
}

func (s *Service) Delete(ctx context.Context, device, id uuid.UUID) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx,
		`UPDATE tasks SET deleted_at=now(), hlc=$2, version=version+1 WHERE id=$1 AND deleted_at IS NULL`,
		id, s.feed.Clock.Now())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if _, err := s.feed.Append(ctx, tx, "task", id, "delete", device); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ------------------------------------------------------------------- reads

const selectTask = `
	SELECT t.id, t.title, t.notes, t.state, t.priority, t.due_at, t.all_day,
	       t.note_id, t.event_id, t.message_id,
	       coalesce(t.recur_every,''), t.recur_interval, t.completed_at,
	       t.version, t.hlc, t.created_at, t.updated_at,
	       coalesce(sj.subs, '{}') AS subjects
	  FROM tasks t
	  LEFT JOIN LATERAL (
	    SELECT array_agg(ss.slug ORDER BY ss.confidence DESC) AS subs
	      FROM source_subjects ss
	     WHERE ss.source_type='task' AND ss.source_id = t.id
	  ) sj ON true`

func (s *Service) scan(rows pgx.Rows) ([]Task, error) {
	defer rows.Close()
	out := []Task{}
	byID := map[uuid.UUID]int{}
	for rows.Next() {
		var t Task
		if err := rows.Scan(&t.ID, &t.Title, &t.Notes, &t.State, &t.Priority, &t.DueAt,
			&t.AllDay, &t.NoteID, &t.EventID, &t.MessageID,
			&t.RecurEvery, &t.RecurInterval, &t.CompletedAt,
			&t.Version, &t.HLC, &t.CreatedAt, &t.UpdatedAt, &t.Subjects); err != nil {
			return nil, err
		}
		t.Reminders = []Reminder{}
		byID[t.ID] = len(out)
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// attachReminders fills in reminders for a page of tasks in one query rather
// than one per row.
func (s *Service) attachReminders(ctx context.Context, list []Task) error {
	if len(list) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(list))
	at := map[uuid.UUID]int{}
	for i, t := range list {
		ids[i] = t.ID
		at[t.ID] = i
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id, task_id, fire_at, fired_at FROM task_reminders
		  WHERE task_id = ANY($1) ORDER BY fire_at`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var r Reminder
		var tid uuid.UUID
		if err := rows.Scan(&r.ID, &tid, &r.FireAt, &r.FiredAt); err != nil {
			return err
		}
		if i, ok := at[tid]; ok {
			list[i].Reminders = append(list[i].Reminders, r)
		}
	}
	return rows.Err()
}

func (s *Service) Get(ctx context.Context, id uuid.UUID) (Task, error) {
	rows, err := s.pool.Query(ctx, selectTask+` WHERE t.id=$1 AND t.deleted_at IS NULL`, id)
	if err != nil {
		return Task{}, err
	}
	list, err := s.scan(rows)
	if err != nil {
		return Task{}, err
	}
	if len(list) == 0 {
		return Task{}, ErrNotFound
	}
	if err := s.attachReminders(ctx, list); err != nil {
		return Task{}, err
	}
	return list[0], nil
}

// List returns tasks for one of the home screen's views.
//
//	open      everything not finished, overdue first, then dated, then undated
//	today     overdue plus anything due before tomorrow
//	upcoming  dated, after today
//	done      recently completed
func (s *Service) List(ctx context.Context, view string, loc *time.Location, limit int) ([]Task, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	now := time.Now().In(loc)
	endOfToday := time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 59, 0, loc)

	var where, order string
	args := []any{limit}
	switch view {
	case "today":
		where = `t.state='open' AND t.due_at IS NOT NULL AND t.due_at <= $2`
		order = `t.due_at`
		args = append(args, endOfToday)
	case "upcoming":
		where = `t.state='open' AND t.due_at > $2`
		order = `t.due_at`
		args = append(args, endOfToday)
	case "done":
		where = `t.state <> 'open'`
		order = `t.completed_at DESC NULLS LAST, t.updated_at DESC`
	default: // open
		where = `t.state='open'`
		// Dated before undated, soonest first, then priority. NULLS LAST is
		// what puts the someday pile below anything with a date on it.
		order = `t.due_at NULLS LAST, t.priority DESC, t.created_at`
	}

	rows, err := s.pool.Query(ctx,
		selectTask+` WHERE t.deleted_at IS NULL AND `+where+` ORDER BY `+order+` LIMIT $1`, args...)
	if err != nil {
		return nil, err
	}
	list, err := s.scan(rows)
	if err != nil {
		return nil, err
	}
	if err := s.attachReminders(ctx, list); err != nil {
		return nil, err
	}
	return list, nil
}

type Counts struct {
	Open    int `json:"open"`
	Overdue int `json:"overdue"`
	Today   int `json:"today"`
	Done    int `json:"done"`
}

func (s *Service) Counts(ctx context.Context, loc *time.Location) (Counts, error) {
	now := time.Now().In(loc)
	end := time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 59, 0, loc)
	var c Counts
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE state='open'),
		       count(*) FILTER (WHERE state='open' AND due_at < now()),
		       count(*) FILTER (WHERE state='open' AND due_at IS NOT NULL AND due_at <= $1),
		       count(*) FILTER (WHERE state='done')
		  FROM tasks WHERE deleted_at IS NULL`, end).
		Scan(&c.Open, &c.Overdue, &c.Today, &c.Done)
	return c, err
}

// FromEvent turns an extracted calendar event into a task.
//
// Idempotent: a second tap returns the task the first one made rather than a
// duplicate. The Review queue is a list you work down, and double-tapping a
// row there is normal.
func (s *Service) FromEvent(ctx context.Context, device, eventID uuid.UUID) (Task, bool, error) {
	var existing uuid.UUID
	err := s.pool.QueryRow(ctx,
		`SELECT id FROM tasks WHERE event_id=$1 AND deleted_at IS NULL LIMIT 1`, eventID).
		Scan(&existing)
	if err == nil {
		t, err := s.Get(ctx, existing)
		return t, false, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Task{}, false, err
	}

	var (
		title    string
		startsAt time.Time
		allDay   bool
		location string
		msgID    *uuid.UUID
	)
	if err := s.pool.QueryRow(ctx, `
		SELECT title, starts_at, all_day, location, source_message
		  FROM events WHERE id=$1 AND deleted_at IS NULL`, eventID).
		Scan(&title, &startsAt, &allDay, &location, &msgID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Task{}, false, ErrNotFound
		}
		return Task{}, false, err
	}

	notes := ""
	if location != "" {
		notes = "Where: " + location
	}
	t, err := s.Put(ctx, device, PutInput{
		Title:     title,
		Notes:     notes,
		DueAt:     &startsAt,
		AllDay:    &allDay,
		EventID:   &eventID,
		MessageID: msgID,
		// A deadline you have chosen to track is worth one nudge the day
		// before; the event itself already shows on the calendar.
		RemindAt: dayBefore(startsAt),
	})
	return t, true, err
}

func dayBefore(t time.Time) []time.Time {
	at := t.AddDate(0, 0, -1)
	if at.Before(time.Now()) {
		return nil
	}
	return []time.Time{at}
}
