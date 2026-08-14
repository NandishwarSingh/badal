// Package timetable owns the recurring weekly schedule: classes and the study
// blocks around them.
//
// A slot is stored as a weekday plus minutes-from-midnight in the timetable's
// timezone, because it recurs -- there is no single date to anchor it to. The
// "what is on now" question the UI asks is answered on the client against the
// wall clock, so this package stays a plain CRUD store over one term's slots.
//
// Writes are version-checked exactly like tasks: last write wins, and the
// check catches a stale client rather than parking a conflict. A slot is a
// short structured record with nothing to merge by hand.
package timetable

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
	ErrNotFound = errors.New("timetable slot not found")
	ErrConflict = errors.New("version conflict")
	ErrNoActive = errors.New("no active timetable")
)

type Timetable struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	TZ        string    `json:"tz"`
	Active    bool      `json:"active"`
	Version   int       `json:"version"`
	HLC       string    `json:"hlc"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Slot struct {
	ID          uuid.UUID `json:"id"`
	TimetableID uuid.UUID `json:"timetable_id"`
	Kind        string    `json:"kind"`
	Title       string    `json:"title"`
	CourseCode  string    `json:"course_code,omitempty"`
	Location    string    `json:"location,omitempty"`
	DayOfWeek   int       `json:"day_of_week"` // 0 = Monday
	StartsMin   int       `json:"starts_min"`  // minutes from local midnight
	EndsMin     int       `json:"ends_min"`    // may exceed 1440 for a past-midnight block
	Notes       string    `json:"notes,omitempty"`
	Version     int       `json:"version"`
	HLC         string    `json:"hlc"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Service struct {
	pool *pgxpool.Pool
	feed *syncfeed.Feed
}

func New(pool *pgxpool.Pool, feed *syncfeed.Feed) *Service {
	return &Service{pool: pool, feed: feed}
}

// ------------------------------------------------------------------- reads

// Active returns the current timetable and all its live slots, ordered as the
// week runs. One round trip: the slot list is what every client caches.
func (s *Service) Active(ctx context.Context) (Timetable, []Slot, error) {
	var tt Timetable
	err := s.pool.QueryRow(ctx, `
		SELECT id, name, tz, active, version, hlc, created_at, updated_at
		  FROM timetables
		 WHERE active AND deleted_at IS NULL
		 LIMIT 1`).
		Scan(&tt.ID, &tt.Name, &tt.TZ, &tt.Active, &tt.Version, &tt.HLC, &tt.CreatedAt, &tt.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Timetable{}, nil, ErrNoActive
	}
	if err != nil {
		return Timetable{}, nil, err
	}
	slots, err := s.slotsFor(ctx, tt.ID)
	return tt, slots, err
}

const slotCols = `id, timetable_id, kind, title, course_code, location,
                  day_of_week, starts_min, ends_min, notes, version, hlc,
                  created_at, updated_at`

func scanSlots(rows pgx.Rows) ([]Slot, error) {
	defer rows.Close()
	out := []Slot{}
	for rows.Next() {
		var s Slot
		if err := rows.Scan(&s.ID, &s.TimetableID, &s.Kind, &s.Title, &s.CourseCode,
			&s.Location, &s.DayOfWeek, &s.StartsMin, &s.EndsMin, &s.Notes,
			&s.Version, &s.HLC, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (s *Service) slotsFor(ctx context.Context, ttID uuid.UUID) ([]Slot, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+slotCols+` FROM timetable_slots
		  WHERE timetable_id=$1 AND deleted_at IS NULL
		  ORDER BY day_of_week, starts_min`, ttID)
	if err != nil {
		return nil, err
	}
	return scanSlots(rows)
}

func (s *Service) getSlot(ctx context.Context, id uuid.UUID) (Slot, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+slotCols+` FROM timetable_slots WHERE id=$1 AND deleted_at IS NULL`, id)
	if err != nil {
		return Slot{}, err
	}
	list, err := scanSlots(rows)
	if err != nil {
		return Slot{}, err
	}
	if len(list) == 0 {
		return Slot{}, ErrNotFound
	}
	return list[0], nil
}

func (s *Service) activeID(ctx context.Context, tx pgx.Tx) (uuid.UUID, error) {
	var id uuid.UUID
	err := tx.QueryRow(ctx,
		`SELECT id FROM timetables WHERE active AND deleted_at IS NULL LIMIT 1`).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNoActive
	}
	return id, err
}

// ------------------------------------------------------------------ writes

type PutSlotInput struct {
	ID          *uuid.UUID `json:"id"`
	TimetableID *uuid.UUID `json:"timetable_id"`
	Kind        string     `json:"kind"`
	Title       string     `json:"title"`
	CourseCode  string     `json:"course_code"`
	Location    string     `json:"location"`
	DayOfWeek   int        `json:"day_of_week"`
	StartsMin   int        `json:"starts_min"`
	EndsMin     int        `json:"ends_min"`
	Notes       string     `json:"notes"`
	Version     *int       `json:"version"`
}

func (in *PutSlotInput) validate() error {
	if strings.TrimSpace(in.Title) == "" {
		return fmt.Errorf("title is required")
	}
	if in.Kind == "" {
		in.Kind = "class"
	}
	if in.Kind != "class" && in.Kind != "study" {
		return fmt.Errorf("bad kind %q", in.Kind)
	}
	if in.DayOfWeek < 0 || in.DayOfWeek > 6 {
		return fmt.Errorf("day_of_week must be 0..6")
	}
	if in.StartsMin < 0 || in.StartsMin >= 1440 {
		return fmt.Errorf("starts_min must be 0..1439")
	}
	// A block may end after midnight (up to 26:00), but never before it starts.
	if in.EndsMin <= in.StartsMin || in.EndsMin > 1560 {
		return fmt.Errorf("ends_min must be after starts_min and <= 1560")
	}
	return nil
}

// PutSlot creates or updates one slot. Like tasks, an id the client generated
// offline is honoured on a late-arriving insert rather than 404'd.
func (s *Service) PutSlot(ctx context.Context, device uuid.UUID, in PutSlotInput) (Slot, error) {
	if err := in.validate(); err != nil {
		return Slot{}, err
	}
	title := strings.TrimSpace(in.Title)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Slot{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	ttID := uuid.Nil
	if in.TimetableID != nil {
		ttID = *in.TimetableID
	} else {
		ttID, err = s.activeID(ctx, tx)
		if err != nil {
			return Slot{}, err
		}
	}

	hlc := s.feed.Clock.Now()
	var id uuid.UUID

	if in.ID == nil {
		err = tx.QueryRow(ctx, `
			INSERT INTO timetable_slots
			  (timetable_id, kind, title, course_code, location, day_of_week,
			   starts_min, ends_min, notes, hlc)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id`,
			ttID, in.Kind, title, in.CourseCode, in.Location, in.DayOfWeek,
			in.StartsMin, in.EndsMin, in.Notes, hlc).Scan(&id)
		if err != nil {
			return Slot{}, err
		}
	} else {
		id = *in.ID
		var current int
		err = tx.QueryRow(ctx,
			`SELECT version FROM timetable_slots WHERE id=$1 AND deleted_at IS NULL`, id).Scan(&current)
		if errors.Is(err, pgx.ErrNoRows) {
			// A slot written offline already owns the id the client gave it, so
			// an id with no row is a late insert, not a miss.
			if _, err := tx.Exec(ctx, `
				INSERT INTO timetable_slots
				  (id, timetable_id, kind, title, course_code, location, day_of_week,
				   starts_min, ends_min, notes, hlc)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
				ON CONFLICT (id) DO NOTHING`,
				id, ttID, in.Kind, title, in.CourseCode, in.Location, in.DayOfWeek,
				in.StartsMin, in.EndsMin, in.Notes, hlc); err != nil {
				return Slot{}, err
			}
			current = -1
		} else if err != nil {
			return Slot{}, err
		}
		if current >= 0 {
			if in.Version != nil && *in.Version != current {
				return Slot{}, ErrConflict
			}
			if _, err := tx.Exec(ctx, `
				UPDATE timetable_slots
				   SET kind=$2, title=$3, course_code=$4, location=$5, day_of_week=$6,
				       starts_min=$7, ends_min=$8, notes=$9,
				       version=version+1, hlc=$10, updated_at=now()
				 WHERE id=$1`,
				id, in.Kind, title, in.CourseCode, in.Location, in.DayOfWeek,
				in.StartsMin, in.EndsMin, in.Notes, hlc); err != nil {
				return Slot{}, err
			}
		}
	}

	if _, err := s.feed.Append(ctx, tx, "timetable_slot", id, "upsert", device); err != nil {
		return Slot{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Slot{}, err
	}
	return s.getSlot(ctx, id)
}

func (s *Service) DeleteSlot(ctx context.Context, device, id uuid.UUID) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx,
		`UPDATE timetable_slots SET deleted_at=now(), hlc=$2, version=version+1
		  WHERE id=$1 AND deleted_at IS NULL`, id, s.feed.Clock.Now())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if _, err := s.feed.Append(ctx, tx, "timetable_slot", id, "delete", device); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
