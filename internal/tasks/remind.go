package tasks

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// The reminder scheduler.
//
// Server-side on purpose. A timer in the page only runs while the page is
// open, which is precisely when you do not need reminding; the whole value is
// that it reaches a locked phone with the app closed. Web Push does that, so
// the server has to be the thing that knows the time has come.
//
// The sweep is deliberately dumb: find what is due and not yet sent, send it,
// mark it. Marking happens before the push rather than after, because a
// duplicate notification is a nuisance and a re-send loop after a delivery
// error is much worse.

const (
	sweepEvery = 30 * time.Second

	// How late a reminder may be and still be worth sending. Past this the
	// moment has gone, and a notification for a meeting that ended two hours
	// ago is noise -- it stays marked so it never fires again.
	staleAfter = 2 * time.Hour
)

// Pusher is the slice of the push service this needs, kept narrow so tasks
// does not depend on the whole thing.
type Pusher interface {
	SendTask(ctx context.Context, taskID uuid.UUID, title, body string) (int, error)
}

type due struct {
	reminderID uuid.UUID
	taskID     uuid.UUID
	title      string
	fireAt     time.Time
	dueAt      *time.Time
}

// RunReminders sweeps until the context is cancelled.
func (s *Service) RunReminders(ctx context.Context, p Pusher, loc *time.Location) {
	t := time.NewTicker(sweepEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n, err := s.sweep(ctx, p, loc); err != nil {
				slog.Warn("reminder sweep", "err", err)
			} else if n > 0 {
				slog.Info("reminders sent", "count", n)
			}
		}
	}
}

func (s *Service) sweep(ctx context.Context, p Pusher, loc *time.Location) (int, error) {
	// Claim and mark in one statement. Two servers, or a restart mid-sweep,
	// cannot then send the same reminder twice.
	rows, err := s.pool.Query(ctx, `
		WITH claimed AS (
		  SELECT r.id
		    FROM task_reminders r
		    JOIN tasks t ON t.id = r.task_id
		   WHERE r.fired_at IS NULL
		     AND r.fire_at <= now()
		     AND t.deleted_at IS NULL
		     AND t.state = 'open'
		   ORDER BY r.fire_at
		   LIMIT 50
		   FOR UPDATE OF r SKIP LOCKED
		)
		UPDATE task_reminders r
		   SET fired_at = now()
		  FROM claimed c, tasks t
		 WHERE r.id = c.id AND t.id = r.task_id
		 RETURNING r.id, t.id, t.title, r.fire_at, t.due_at`)
	if err != nil {
		return 0, err
	}
	var batch []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.reminderID, &d.taskID, &d.title, &d.fireAt, &d.dueAt); err != nil {
			rows.Close()
			return 0, err
		}
		batch = append(batch, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	// Reminders whose task was finished or deleted are swept up separately:
	// they can never be worth sending, and left unfired they would be
	// reconsidered on every pass forever.
	if _, err := s.pool.Exec(ctx, `
		UPDATE task_reminders r SET fired_at = now()
		  FROM tasks t
		 WHERE r.task_id = t.id AND r.fired_at IS NULL AND r.fire_at <= now()
		   AND (t.deleted_at IS NOT NULL OR t.state <> 'open')`); err != nil {
		slog.Warn("retire reminders", "err", err)
	}

	sent := 0
	for _, d := range batch {
		if time.Since(d.fireAt) > staleAfter {
			slog.Info("reminder skipped as stale", "task", d.taskID, "late", time.Since(d.fireAt))
			continue
		}
		if _, err := p.SendTask(ctx, d.taskID, d.title, dueLine(d.dueAt, loc)); err != nil {
			// Already marked fired, so this is not retried. Losing one
			// notification beats a loop that sends it every 30 seconds.
			slog.Warn("reminder push", "task", d.taskID, "err", err)
			continue
		}
		sent++
	}
	return sent, nil
}

// dueLine is the notification's second line: when the thing is actually due,
// phrased the way you would say it rather than as a timestamp.
func dueLine(dueAt *time.Time, loc *time.Location) string {
	if dueAt == nil {
		return "Reminder"
	}
	d := dueAt.In(loc)
	now := time.Now().In(loc)
	day := func(t time.Time) time.Time {
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
	}
	days := int(day(d).Sub(day(now)).Hours() / 24)
	at := d.Format("15:04")
	switch {
	case days < 0:
		return "Overdue — was due " + d.Format("2 Jan")
	case days == 0:
		return "Due today at " + at
	case days == 1:
		return "Due tomorrow at " + at
	case days < 7:
		return "Due " + d.Format("Monday") + " at " + at
	default:
		return "Due " + d.Format("2 Jan")
	}
}
