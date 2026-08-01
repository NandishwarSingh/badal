package exam

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// The academic calendar: the shape of the semester, imported from the circular.
//
// It answers "which week" and never "which hour". CAT-I runs 9-16 August; the
// paper that matters is somewhere inside that, and only VTOP knows where. So
// the calendar is what makes planning possible before VTOP publishes anything,
// which this term is the whole game -- the window opens eight days from the
// day it was imported and the schedule was not out yet.
//
// Re-importable on purpose. Circulars get reissued, dates move, and the winter
// calendar arrives in December. The unique index is (source, label, start), so
// a second import of the same circular updates rather than duplicating.

// Day is a calendar date with no time and no zone, which is what a circular
// actually publishes. Parsing "09.08.2026" into an instant would invent a time
// nobody stated and shift the date the moment a timezone was applied.
type Day struct{ time.Time }

func (d *Day) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return fmt.Errorf("bad date %q, want YYYY-MM-DD", s)
	}
	d.Time = t
	return nil
}

func (d Day) MarshalJSON() ([]byte, error) {
	return []byte(`"` + d.Format("2006-01-02") + `"`), nil
}

type Entry struct {
	Kind     string `json:"kind"` // semester | exam_window | holiday | instructional | event
	Label    string `json:"label"`
	StartsOn Day    `json:"starts_on"`
	// Optional; a single day when absent, so callers never have to special-case
	// the difference between a day and a span.
	EndsOn   *Day   `json:"ends_on,omitempty"`
	ExamKind string `json:"exam_kind,omitempty"` // CAT-1 | CAT-2 | FAT | Quiz | Lab
	// A Monday timetable run on a Saturday to make up a lost day. VTOP does
	// this several times a term and it silently breaks any planner that
	// assumes the weekend is free.
	DayOrder int `json:"day_order,omitempty"` // ISO weekday, 1 = Monday
}

type CalendarResult struct {
	Imported int      `json:"imported"`
	Problems []string `json:"problems,omitempty"`
}

// ImportCalendar writes a circular's dates, replacing any previous import of
// the same source.
//
// Partial results are deliberate: one malformed row in a twenty-four row
// circular should not cost the other twenty-three, and the row that failed is
// named so it can be fixed.
func (s *Service) ImportCalendar(ctx context.Context, entries []Entry, source string) (CalendarResult, error) {
	var r CalendarResult
	if source == "" {
		source = "calendar"
	}

	for i, e := range entries {
		label := strings.TrimSpace(e.Label)
		if label == "" || e.StartsOn.IsZero() {
			r.Problems = append(r.Problems, fmt.Sprintf("row %d: needs a label and a start date", i+1))
			continue
		}
		end := e.StartsOn
		if e.EndsOn != nil {
			end = *e.EndsOn
		}
		if end.Before(e.StartsOn.Time) {
			r.Problems = append(r.Problems, label+": ends before it starts")
			continue
		}

		var examKind, dayOrder any
		if e.ExamKind != "" {
			examKind = e.ExamKind
		}
		if e.DayOrder != 0 {
			dayOrder = e.DayOrder
		}

		if _, err := s.pool.Exec(ctx, `
			INSERT INTO calendar_entries (kind, label, starts_on, ends_on, exam_kind, day_order, source)
			VALUES ($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT (source, lower(label), starts_on) DO UPDATE
			   SET kind=EXCLUDED.kind, ends_on=EXCLUDED.ends_on,
			       exam_kind=EXCLUDED.exam_kind, day_order=EXCLUDED.day_order`,
			e.Kind, label, e.StartsOn.Time, end.Time, examKind, dayOrder, source); err != nil {
			r.Problems = append(r.Problems, label+": "+err.Error())
			continue
		}
		r.Imported++
	}

	slog.Info("calendar imported", "source", source, "rows", r.Imported, "problems", len(r.Problems))
	return r, nil
}

// ------------------------------------------------------------------ reading

type Window struct {
	ID       string `json:"id"`
	ExamKind string `json:"exam_kind"`
	Label    string `json:"label"`
	StartsOn Day    `json:"starts_on"`
	EndsOn   Day    `json:"ends_on"`
	// Whole days from today to the first day of the window. Negative once it
	// has started, which is a state the UI has to show differently rather than
	// clamp to zero and pretend there is still time.
	DaysAway int `json:"days_away"`
	// Instructional days left before it starts, holidays and weekends removed.
	// The number that actually matters: eight days away can be four days of
	// work, and a plan built on the larger number is a plan that fails.
	FreeDays int `json:"free_days"`
}

// NextWindow returns the next exam window that has not finished.
func (s *Service) NextWindow(ctx context.Context, kind string) (*Window, error) {
	today := time.Now().Truncate(24 * time.Hour)

	var w Window
	var starts, ends time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT id, coalesce(exam_kind,''), label, starts_on, ends_on
		  FROM calendar_entries
		 WHERE kind = 'exam_window'
		   AND ends_on >= current_date
		   AND ($1 = '' OR exam_kind = $1)
		 ORDER BY starts_on
		 LIMIT 1`, kind).Scan(&w.ID, &w.ExamKind, &w.Label, &starts, &ends)
	if err != nil {
		return nil, err
	}
	w.StartsOn, w.EndsOn = Day{starts}, Day{ends}
	w.DaysAway = int(starts.Sub(today).Hours() / 24)

	free, err := s.freeDays(ctx, today, starts)
	if err != nil {
		return nil, err
	}
	w.FreeDays = free
	return &w, nil
}

// freeDays counts days between now and an exam that are actually available:
// every date in the range, minus the ones a holiday or another exam window
// already claims.
//
// Weekends are not removed. A Sunday before CAT-I is the most useful day there
// is, and this term CAT-I itself starts on one.
func (s *Service) freeDays(ctx context.Context, from, to time.Time) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM generate_series($1::date, $2::date - 1, '1 day') AS d
		 WHERE NOT EXISTS (
		     SELECT 1 FROM calendar_entries c
		      WHERE c.kind IN ('holiday', 'exam_window')
		        AND d.d BETWEEN c.starts_on AND c.ends_on)`, from, to).Scan(&n)
	return n, err
}

// Upcoming returns everything on the calendar from today onwards.
func (s *Service) Upcoming(ctx context.Context, limit int) ([]Entry, error) {
	if limit <= 0 || limit > 200 {
		limit = 60
	}
	rows, err := s.pool.Query(ctx, `
		SELECT kind, label, starts_on, ends_on, coalesce(exam_kind,''), coalesce(day_order,0)
		  FROM calendar_entries
		 WHERE ends_on >= current_date
		 ORDER BY starts_on
		 LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Entry{}
	for rows.Next() {
		var e Entry
		var starts, ends time.Time
		if err := rows.Scan(&e.Kind, &e.Label, &starts, &ends, &e.ExamKind, &e.DayOrder); err != nil {
			return nil, err
		}
		e.StartsOn = Day{starts}
		if !ends.Equal(starts) {
			d := Day{ends}
			e.EndsOn = &d
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
