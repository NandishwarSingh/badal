// Package surreal maintains the derived knowledge graph in SurrealDB.
//
// Postgres is the source of truth (see internal/notes). Everything here is a
// projection driven off the change feed, so SurrealDB can be wiped and rebuilt
// by resetting projector_state.last_seq to 0. That is what makes running two
// stores without distributed transactions safe: they cannot permanently
// disagree, because one is definitionally derived from the other.
package surreal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NandishwarSingh/badal/internal/notes"
)

type Client struct {
	url  string
	user string
	pass string
	ns   string
	db   string
	http *http.Client
}

func NewClient(url, user, pass, ns, db string) *Client {
	return &Client{url: url, user: user, pass: pass, ns: ns, db: db,
		http: &http.Client{Timeout: 20 * time.Second}}
}

type rpcReq struct {
	ID     int    `json:"id"`
	Method string `json:"method"`
	Params []any  `json:"params"`
}

type rpcResp struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// Query runs SurrealQL with bound parameters. Using /rpc rather than /sql is
// deliberate: note bodies are arbitrary user text, and concatenating them into
// a query string would be an injection hole.
func (c *Client) Query(ctx context.Context, q string, vars map[string]any) (json.RawMessage, error) {
	if vars == nil {
		vars = map[string]any{}
	}
	body, err := json.Marshal(rpcReq{ID: 1, Method: "query", Params: []any{q, vars}})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url+"/rpc", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.user, c.pass)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("surreal-ns", c.ns)
	req.Header.Set("surreal-db", c.db)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out rpcResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if out.Error != nil {
		return nil, fmt.Errorf("surreal: %s", out.Error.Message)
	}
	return out.Result, nil
}

func (c *Client) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("surreal health: %s", resp.Status)
	}
	return nil
}

// Schema is idempotent so it can run on every boot.
//
// SCHEMALESS on purpose: Phase 5 adds an embedding field and a vector index,
// and a schemaless table lets that land without a migration dance.
const schema = `
DEFINE TABLE IF NOT EXISTS note SCHEMALESS;
DEFINE FIELD IF NOT EXISTS slug       ON note TYPE string;
DEFINE FIELD IF NOT EXISTS title      ON note TYPE string;
DEFINE FIELD IF NOT EXISTS body       ON note TYPE string;
DEFINE FIELD IF NOT EXISTS tags       ON note TYPE array<string> DEFAULT [];
DEFINE FIELD IF NOT EXISTS updated_at ON note TYPE datetime;
DEFINE INDEX IF NOT EXISTS note_slug ON note FIELDS slug UNIQUE;
DEFINE INDEX IF NOT EXISTS note_tags ON note FIELDS tags;
DEFINE TABLE IF NOT EXISTS links SCHEMALESS TYPE RELATION IN note OUT note;
`

func (c *Client) EnsureSchema(ctx context.Context) error {
	_, err := c.Query(ctx, schema, nil)
	return err
}

// ---------------------------------------------------------------- projector

type Projector struct {
	pool  *pgxpool.Pool
	sc    *Client
	notes *notes.Service
}

func NewProjector(pool *pgxpool.Pool, sc *Client, n *notes.Service) *Projector {
	return &Projector{pool: pool, sc: sc, notes: n}
}

// Run projects continuously until ctx is cancelled. It is intentionally a
// simple poll: the change feed is a table, the volume is personal-scale, and a
// poll loop has no missed-notification failure mode to debug at 2am.
func (p *Projector) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n, err := p.Once(ctx); err != nil {
				slog.Warn("projector", "err", err)
			} else if n > 0 {
				slog.Info("projected", "changes", n)
			}
		}
	}
}

// Once drains pending note changes. Returns how many it applied.
func (p *Projector) Once(ctx context.Context) (int, error) {
	var last int64
	if err := p.pool.QueryRow(ctx,
		`SELECT last_seq FROM projector_state WHERE name='surreal'`).Scan(&last); err != nil {
		return 0, err
	}

	rows, err := p.pool.Query(ctx,
		`SELECT seq, entity_id, op FROM change_log
		  WHERE seq > $1 AND entity_type = 'note'
		  ORDER BY seq LIMIT 200`, last)
	if err != nil {
		return 0, err
	}
	type ch struct {
		seq int64
		id  uuid.UUID
		op  string
	}
	batch := []ch{}
	for rows.Next() {
		var c ch
		if err := rows.Scan(&c.seq, &c.id, &c.op); err != nil {
			rows.Close()
			return 0, err
		}
		batch = append(batch, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(batch) == 0 {
		return 0, nil
	}

	applied := 0
	for _, c := range batch {
		if err := p.apply(ctx, c.id, c.op); err != nil {
			// Persist progress up to the last good change so a single poison
			// row does not wedge the projector forever on retry.
			if applied > 0 {
				_ = p.setCursor(ctx, batch[applied-1].seq)
			}
			return applied, fmt.Errorf("seq %d: %w", c.seq, err)
		}
		applied++
	}
	if err := p.setCursor(ctx, batch[len(batch)-1].seq); err != nil {
		return applied, err
	}
	return applied, nil
}

func (p *Projector) setCursor(ctx context.Context, seq int64) error {
	_, err := p.pool.Exec(ctx,
		`UPDATE projector_state SET last_seq=$1, updated_at=now() WHERE name='surreal'`, seq)
	return err
}

func (p *Projector) apply(ctx context.Context, id uuid.UUID, op string) error {
	n, err := p.notes.GetByID(ctx, id)
	// GetByID excludes soft-deleted rows, so "deleted" and "never existed"
	// arrive here identically -- both mean: remove the node.
	if err != nil || op == "delete" {
		_, derr := p.sc.Query(ctx,
			`DELETE type::thing('note', $id); `+
				`DELETE links WHERE in = type::thing('note',$id) OR out = type::thing('note',$id);`,
			map[string]any{"id": id.String()})
		return derr
	}

	if _, err := p.sc.Query(ctx,
		`UPSERT type::thing('note', $id) SET
		   slug=$slug, title=$title, body=$body, tags=$tags, updated_at=time::now();`,
		map[string]any{
			"id": n.ID.String(), "slug": n.Slug, "title": n.Title,
			"body": n.Body, "tags": n.Tags,
		}); err != nil {
		return err
	}

	// Rewrite outgoing edges. Only resolved links become graph edges; dangling
	// ones stay in Postgres until their target exists.
	out, err := p.notes.Outlinks(ctx, n.ID)
	if err != nil {
		return err
	}
	if _, err := p.sc.Query(ctx,
		`DELETE links WHERE in = type::thing('note',$id);`,
		map[string]any{"id": n.ID.String()}); err != nil {
		return err
	}
	for _, l := range out {
		if l.ID == nil {
			continue
		}
		// RELATE will not accept a function call in the record position --
		// it wants a record reference, so bind through LET first.
		if _, err := p.sc.Query(ctx,
			`LET $a = type::thing('note',$src);
			 LET $b = type::thing('note',$dst);
			 RELATE $a->links->$b;`,
			map[string]any{"src": n.ID.String(), "dst": l.ID.String()}); err != nil {
			return err
		}
	}
	return nil
}

// Related walks the graph outward from a note, which is the thing SurrealDB is
// here for -- Phase 5 blends these hits with vector similarity.
func (p *Projector) Related(ctx context.Context, id uuid.UUID, depth int) (json.RawMessage, error) {
	if depth < 1 || depth > 3 {
		depth = 2
	}
	// depth is clamped above and only ever expands a fixed literal, so this
	// Sprintf cannot carry user input into the query.
	hops := ""
	for i := 0; i < depth; i++ {
		hops += "->links->note"
	}
	q := fmt.Sprintf(`LET $n = type::thing('note',$id); SELECT slug, title FROM $n%s;`, hops)
	return p.sc.Query(ctx, q, map[string]any{"id": id.String()})
}
