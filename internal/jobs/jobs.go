// Package jobs is the lease-based work queue the Mac worker pulls from.
//
// The design assumption is that the worker is a laptop and will disappear
// without warning. So nothing depends on it releasing anything: a claim is a
// lease with an expiry, the worker extends it by heartbeating, and a reaper on
// the server requeues whatever stops being heartbeated. Yank the lid mid-job
// and the work is back in the queue inside a minute.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	LeaseTTL     = 90 * time.Second
	ReaperPeriod = 20 * time.Second
)

var ErrNotFound = errors.New("job not found")

type Job struct {
	ID       uuid.UUID       `json:"id"`
	Kind     string          `json:"kind"`
	Payload  json.RawMessage `json:"payload"`
	State    string          `json:"state"`
	Attempts int             `json:"attempts"`
	Result   json.RawMessage `json:"result,omitempty"`
	Error    string          `json:"error,omitempty"`
}

type Service struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

// Enqueue adds work. A payload carrying "key" is deduplicated, so asking for
// the same image twice reuses the first job instead of paying twice.
func (s *Service) Enqueue(ctx context.Context, kind string, payload any, priority int) (uuid.UUID, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return uuid.Nil, err
	}
	if priority == 0 {
		priority = 5
	}
	var id uuid.UUID
	err = s.pool.QueryRow(ctx,
		`INSERT INTO jobs (kind, payload, priority) VALUES ($1,$2,$3)
		 ON CONFLICT (kind, (payload->>'key')) WHERE payload ? 'key' AND state <> 'failed'
		 DO UPDATE SET priority = least(jobs.priority, EXCLUDED.priority)
		 RETURNING id`, kind, b, priority).Scan(&id)
	return id, err
}

// Claim leases up to n jobs of the given kinds.
//
// FOR UPDATE SKIP LOCKED is what makes concurrent workers safe without a
// broker: two claims never see the same row.
func (s *Service) Claim(ctx context.Context, owner string, kinds []string, n int) ([]Job, error) {
	if n <= 0 || n > 8 {
		n = 2
	}
	if len(kinds) == 0 {
		return nil, nil
	}
	// Record presence on every poll, not just successful claims. An idle
	// worker must be distinguishable from an absent one.
	_, _ = s.pool.Exec(ctx,
		`INSERT INTO workers (owner, kinds) VALUES ($1,$2)
		 ON CONFLICT (owner) DO UPDATE
		   SET last_seen=now(), kinds=EXCLUDED.kinds, claims=workers.claims+1`,
		owner, kinds)
	rows, err := s.pool.Query(ctx,
		`UPDATE jobs SET
		   state = 'leased',
		   lease_owner = $1,
		   lease_expires_at = now() + $2::interval,
		   attempts = attempts + 1,
		   started_at = coalesce(started_at, now())
		 WHERE id IN (
		   SELECT id FROM jobs
		    WHERE state = 'queued' AND kind = ANY($3)
		    ORDER BY priority, created_at
		    FOR UPDATE SKIP LOCKED
		    LIMIT $4)
		 RETURNING id, kind, payload, state, attempts`,
		owner, LeaseTTL.String(), kinds, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		var j Job
		if err := rows.Scan(&j.ID, &j.Kind, &j.Payload, &j.State, &j.Attempts); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// Heartbeat extends a lease. It checks ownership so a worker that lost its
// lease to the reaper cannot silently reclaim a job someone else now holds.
func (s *Service) Heartbeat(ctx context.Context, id uuid.UUID, owner string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE jobs SET lease_expires_at = now() + $3::interval
		  WHERE id=$1 AND lease_owner=$2 AND state='leased'`,
		id, owner, LeaseTTL.String())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Service) Complete(ctx context.Context, id uuid.UUID, owner string, result any) error {
	b, err := json.Marshal(result)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE jobs SET state='done', result=$3, finished_at=now(),
		                lease_owner=NULL, lease_expires_at=NULL
		  WHERE id=$1 AND lease_owner=$2 AND state='leased'`, id, owner, b)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Fail records an attempt. Retries go back to queued until max_attempts, then
// the job is abandoned -- retrying forever is how a poison job burns a laptop
// battery for a week.
func (s *Service) Fail(ctx context.Context, id uuid.UUID, owner, reason string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE jobs SET
		   state = CASE WHEN attempts >= max_attempts THEN 'abandoned' ELSE 'queued' END,
		   error = $3,
		   lease_owner = NULL,
		   lease_expires_at = NULL,
		   finished_at = CASE WHEN attempts >= max_attempts THEN now() ELSE NULL END
		 WHERE id=$1 AND lease_owner=$2`, id, owner, reason)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Await waits for a job to finish, for callers that would rather have the
// worker's answer than fall back immediately. Returns ok=false on timeout,
// which is the signal to use the cloud instead.
func (s *Service) Await(ctx context.Context, id uuid.UUID, wait time.Duration) (result json.RawMessage, ok bool) {
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		var state string
		var res []byte
		err := s.pool.QueryRow(ctx,
			`SELECT state, result FROM jobs WHERE id=$1`, id).Scan(&state, &res)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false
		}
		if err == nil {
			switch state {
			case "done":
				return res, true
			case "failed", "abandoned":
				return nil, false
			}
		}
		select {
		case <-ctx.Done():
			return nil, false
		case <-time.After(400 * time.Millisecond):
		}
	}
	return nil, false
}

// Supersede marks that the caller stopped waiting. The job is dropped rather
// than left for the worker to redo work whose answer is no longer wanted.
func (s *Service) Supersede(ctx context.Context, id uuid.UUID) {
	_, _ = s.pool.Exec(ctx,
		`UPDATE jobs SET superseded_at=now(),
		        state = CASE WHEN state='queued' THEN 'done' ELSE state END
		  WHERE id=$1 AND state IN ('queued','leased')`, id)
}

// Reap requeues expired leases. This is the whole recovery story for a worker
// that went away without saying anything.
func (s *Service) Reap(ctx context.Context) (int, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE jobs SET
		   state = CASE WHEN attempts >= max_attempts THEN 'abandoned' ELSE 'queued' END,
		   lease_owner = NULL,
		   lease_expires_at = NULL,
		   error = coalesce(error,'') || ' [lease expired]'
		 WHERE state='leased' AND lease_expires_at < now()`)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (s *Service) RunReaper(ctx context.Context) {
	t := time.NewTicker(ReaperPeriod)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n, err := s.Reap(ctx); err != nil {
				slog.Warn("reaper", "err", err)
			} else if n > 0 {
				slog.Info("requeued expired leases", "jobs", n)
			}
		}
	}
}

type Worker struct {
	Owner    string    `json:"owner"`
	LastSeen time.Time `json:"last_seen"`
	Online   bool      `json:"online"`
}

type Stats struct {
	Queued    int      `json:"queued"`
	Leased    int      `json:"leased"`
	Done      int      `json:"done"`
	Abandoned int      `json:"abandoned"`
	Workers   []Worker `json:"workers"`
	// True when any worker has polled recently. This is what the UI uses to
	// decide between "processing" and "waiting for your Mac".
	Online bool `json:"online"`
}

// Stats powers the "waiting for your Mac" state in the UI. Showing the queue
// is the whole mitigation for a worker that is only sometimes there.
func (s *Service) Stats(ctx context.Context) (Stats, error) {
	var st Stats
	rows, err := s.pool.Query(ctx,
		`SELECT state, count(*) FROM jobs
		  WHERE created_at > now() - interval '7 days' GROUP BY state`)
	if err != nil {
		return st, err
	}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			rows.Close()
			return st, err
		}
		switch k {
		case "queued":
			st.Queued = n
		case "leased":
			st.Leased = n
		case "done":
			st.Done = n
		case "abandoned":
			st.Abandoned = n
		}
	}
	rows.Close()

	st.Workers = []Worker{}
	// Three poll intervals of grace before a worker is called offline, so a
	// single missed tick does not flip the UI to "asleep".
	wr, err := s.pool.Query(ctx,
		`SELECT owner, last_seen, last_seen > now() - interval '20 seconds'
		   FROM workers ORDER BY last_seen DESC`)
	if err == nil {
		for wr.Next() {
			var w Worker
			if wr.Scan(&w.Owner, &w.LastSeen, &w.Online) == nil {
				st.Workers = append(st.Workers, w)
				if w.Online {
					st.Online = true
				}
			}
		}
		wr.Close()
	}
	return st, nil
}

func (s *Service) Purge(ctx context.Context) {
	_, _ = s.pool.Exec(ctx,
		`DELETE FROM jobs WHERE state IN ('done','abandoned')
		   AND finished_at < now() - interval '14 days'`)
	_ = fmt.Sprint
}
