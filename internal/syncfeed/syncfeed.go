// Package syncfeed owns the change feed -- the backbone every client syncs from.
//
// Clients hold a monotonic `seq` cursor and pull changes since it. The
// websocket (later) only rings a doorbell saying "new seq available"; it never
// carries data. That collapses reconnect, missed messages and cold start into
// one code path.
package syncfeed

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Clock is a hybrid logical clock. Wall time alone is not enough: two devices
// with skewed clocks would order writes wrongly, and a clock that jumps
// backwards would let an older write win. The counter breaks ties and keeps
// the sequence monotonic even when the wall clock misbehaves.
type Clock struct {
	mu      sync.Mutex
	nodeID  string
	lastMs  int64
	counter uint16
}

func NewClock(nodeID string) *Clock { return &Clock{nodeID: nodeID} }

func (c *Clock) Now() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	ms := time.Now().UnixMilli()
	if ms > c.lastMs {
		c.lastMs, c.counter = ms, 0
	} else {
		c.counter++ // same millisecond, or wall clock went backwards
	}
	// zero-padded so lexical order matches chronological order
	return fmt.Sprintf("%013d.%05d.%s", c.lastMs, c.counter, c.nodeID)
}

type Change struct {
	Seq        int64     `json:"seq"`
	EntityType string    `json:"entity_type"`
	EntityID   uuid.UUID `json:"entity_id"`
	Op         string    `json:"op"`
	HLC        string    `json:"hlc"`
	CreatedAt  time.Time `json:"created_at"`
}

type Feed struct {
	pool  *pgxpool.Pool
	Clock *Clock
}

func New(pool *pgxpool.Pool, nodeID string) *Feed {
	return &Feed{pool: pool, Clock: NewClock(nodeID)}
}

// Append records a mutation. It takes a pgx.Tx so the change row commits in
// the same transaction as the mutation itself -- otherwise a crash between the
// two would leave a change clients can never reconcile, or a mutation they
// never learn about.
func (f *Feed) Append(ctx context.Context, tx pgx.Tx, entityType string, id uuid.UUID, op string, device uuid.UUID) (string, error) {
	hlc := f.Clock.Now()
	_, err := tx.Exec(ctx,
		`INSERT INTO change_log (entity_type, entity_id, op, actor_device, hlc)
		 VALUES ($1,$2,$3,$4,$5)`, entityType, id, op, device, hlc)
	if err != nil {
		return "", err
	}
	return hlc, nil
}

// Since returns changes after the cursor. limit is capped so a client with a
// stale cursor cannot ask the server to materialise the entire history at once.
func (f *Feed) Since(ctx context.Context, seq int64, limit int) ([]Change, int64, error) {
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	rows, err := f.pool.Query(ctx,
		`SELECT seq, entity_type, entity_id, op, hlc, created_at
		 FROM change_log WHERE seq > $1 ORDER BY seq LIMIT $2`, seq, limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := []Change{}
	for rows.Next() {
		var c Change
		if err := rows.Scan(&c.Seq, &c.EntityType, &c.EntityID, &c.Op, &c.HLC, &c.CreatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	var head int64
	if err := f.pool.QueryRow(ctx, `SELECT coalesce(max(seq),0) FROM change_log`).Scan(&head); err != nil {
		return nil, 0, err
	}
	return out, head, nil
}
