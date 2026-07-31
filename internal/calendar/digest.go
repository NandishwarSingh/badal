package calendar

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Digest is the one notification a day that ties the system together: what is
// happening today, what is due this week, and anything still waiting on you.
type Digest struct {
	Title    string `json:"title"`
	Body     string `json:"body"`
	Today    int    `json:"today"`
	Week     int    `json:"week"`
	Proposed int    `json:"proposed"`
	Tasks    int    `json:"tasks"`
	Empty    bool   `json:"empty"`
}

// BuildDigest summarises the coming week in the user's timezone. Day
// boundaries must be local, or "today" silently means UTC and the 6am digest
// reports yesterday's evening events.
func (s *Service) BuildDigest(ctx context.Context, loc *time.Location) (Digest, error) {
	var d Digest
	now := time.Now().In(loc)
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	dayEnd := dayStart.AddDate(0, 0, 1)
	weekEnd := dayStart.AddDate(0, 0, 7)

	today, err := s.Range(ctx, dayStart, dayEnd, false)
	if err != nil {
		return d, err
	}
	week, err := s.Range(ctx, dayEnd, weekEnd, false)
	if err != nil {
		return d, err
	}
	prop, err := s.Proposed(ctx, 200)
	if err != nil {
		return d, err
	}

	// Tasks read straight from the table rather than through the tasks service:
	// calendar importing tasks, which already references events, would be a
	// cycle for two columns.
	var due []string
	rows, err := s.pool.Query(ctx, `
		SELECT title, due_at FROM tasks
		 WHERE deleted_at IS NULL AND state='open'
		   AND due_at IS NOT NULL AND due_at < $1
		 ORDER BY due_at LIMIT 12`, dayEnd)
	if err == nil {
		for rows.Next() {
			var title string
			var at time.Time
			if err := rows.Scan(&title, &at); err == nil {
				mark := "  "
				if at.Before(now) {
					mark = "! " // overdue, and it should look different
				}
				due = append(due, mark+trim(title, 42))
			}
		}
		rows.Close()
	}

	d.Today, d.Week, d.Proposed, d.Tasks = len(today), len(week), len(prop), len(due)

	var lines []string
	if len(today) > 0 {
		for _, e := range today {
			when := "all day"
			if !e.AllDay {
				when = e.StartsAt.In(loc).Format("15:04")
			}
			lines = append(lines, fmt.Sprintf("%s  %s", when, trim(e.Title, 42)))
		}
	}

	if len(due) > 0 {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, "To do:")
		lines = append(lines, due...)
	}

	// Deadlines get their own section: a deadline buried in a list of talks is
	// the one thing you cannot afford to skim past.
	var deadlines []string
	for _, e := range week {
		if e.Kind == "deadline" || e.Kind == "exam" {
			deadlines = append(deadlines,
				fmt.Sprintf("%s  %s", e.StartsAt.In(loc).Format("Mon 02"), trim(e.Title, 40)))
		}
	}
	if len(deadlines) > 0 {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, "Due this week:")
		lines = append(lines, deadlines...)
	}

	switch {
	case len(today) == 0 && len(deadlines) == 0 && d.Proposed == 0 && len(due) == 0:
		d.Empty = true
		return d, nil
	case len(today) == 0 && len(due) > 0:
		d.Title = fmt.Sprintf("%d to do today", len(due))
	case len(today) == 0:
		d.Title = "Nothing on today"
	case len(today) == 1:
		d.Title = "1 thing today"
	default:
		d.Title = fmt.Sprintf("%d things today", len(today))
	}
	if d.Proposed > 0 {
		lines = append(lines, "", fmt.Sprintf("%d new to review", d.Proposed))
	}
	d.Body = strings.Join(lines, "\n")
	return d, nil
}

func trim(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
