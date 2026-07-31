package tasks

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Repeating tasks.
//
// Completing one closes it and inserts the next occurrence. Advancing the
// existing row in place would be less code, but then the Done list shows a
// single task that has quietly moved eleven times and you can never see that
// you did in fact pay the mess bill in June.

// next advances a due date by one interval.
//
// AddDate is the right tool despite its edge behaviour: adding a month to
// 31 January lands on 3 March, which is wrong for a calendar but harmless
// here, because the next occurrence is then clamped forward from a real date
// rather than compounding. Days and weeks are exact.
func next(due time.Time, every string, interval int, loc *time.Location) time.Time {
	if interval < 1 {
		interval = 1
	}
	d := due.In(loc)
	switch every {
	case "day":
		return d.AddDate(0, 0, interval)
	case "week":
		return d.AddDate(0, 0, 7*interval)
	case "month":
		return d.AddDate(0, interval, 0)
	case "year":
		return d.AddDate(interval, 0, 0)
	}
	return d
}

// advance rolls forward until the result is in the future.
//
// Finishing Monday's weekly task on Wednesday should schedule next Monday, not
// the Monday that has already gone -- and a task ignored for two months should
// come back once, not sixty times.
func advance(due time.Time, every string, interval int, loc *time.Location) time.Time {
	out := next(due, every, interval, loc)
	now := time.Now()
	for i := 0; i < 500 && !out.After(now); i++ {
		out = next(out, every, interval, loc)
	}
	return out
}

// spawnNext inserts the following occurrence of a completed repeating task.
// Returns the new id, or uuid.Nil when the task does not repeat.
func (s *Service) spawnNext(ctx context.Context, tx pgx.Tx, id uuid.UUID, loc *time.Location) (uuid.UUID, error) {
	var (
		title, notes, every string
		priority, interval  int
		dueAt               *time.Time
		allDay              bool
		noteID, eventID     *uuid.UUID
		msgID               *uuid.UUID
	)
	err := tx.QueryRow(ctx, `
		SELECT title, notes, priority, due_at, all_day, coalesce(recur_every,''),
		       recur_interval, note_id, event_id, message_id
		  FROM tasks WHERE id=$1`, id).
		Scan(&title, &notes, &priority, &dueAt, &allDay, &every,
			&interval, &noteID, &eventID, &msgID)
	if err != nil {
		return uuid.Nil, err
	}
	if every == "" || dueAt == nil {
		return uuid.Nil, nil
	}

	newDue := advance(*dueAt, every, interval, loc)
	var newID uuid.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO tasks (title, notes, state, priority, due_at, all_day,
		                   note_id, event_id, message_id,
		                   recur_every, recur_interval, recur_from, hlc)
		VALUES ($1,$2,'open',$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		RETURNING id`,
		title, notes, priority, newDue, allDay, noteID, eventID, msgID,
		every, interval, id, s.feed.Clock.Now()).Scan(&newID); err != nil {
		return uuid.Nil, err
	}

	// Reminders come along at the same offsets from the due date. Copying the
	// absolute times would put every one of them in the past.
	rows, err := tx.Query(ctx,
		`SELECT fire_at FROM task_reminders WHERE task_id=$1 ORDER BY fire_at`, id)
	if err != nil {
		return uuid.Nil, err
	}
	var offsets []time.Duration
	for rows.Next() {
		var at time.Time
		if err := rows.Scan(&at); err != nil {
			rows.Close()
			return uuid.Nil, err
		}
		offsets = append(offsets, dueAt.Sub(at))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return uuid.Nil, err
	}
	for _, off := range offsets {
		at := newDue.Add(-off)
		if at.Before(time.Now()) {
			continue // its moment has already gone for this occurrence
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO task_reminders (task_id, fire_at) VALUES ($1,$2)`, newID, at); err != nil {
			return uuid.Nil, err
		}
	}
	return newID, nil
}
