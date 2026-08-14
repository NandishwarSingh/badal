// Package roadmap turns a subject's uploaded PDFs into a checkable tree.
//
// One roadmap per subject. The documents are converted to Markdown by anydoc,
// merged, and turned into a hierarchy of topics and leaf items by a
// coverage-checked LLM pass (see generate.go). The tree is stored flat with a
// parent pointer and a depth, which is all the UI needs to render and all a
// tick needs to update.
package roadmap

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NandishwarSingh/badal/internal/anydoc"
	"github.com/NandishwarSingh/badal/internal/syncfeed"
)

// GenVersion is baked into every generated roadmap. Bump it when the prompt or
// tree shape changes, so a regenerate is recognisably newer.
const GenVersion = 1

var (
	ErrNotFound = errors.New("roadmap not found")
	ErrConflict = errors.New("version conflict")
	ErrNoDocs   = errors.New("no readable text in the uploaded files")
)

type Roadmap struct {
	ID         uuid.UUID `json:"id"`
	Subject    string    `json:"subject"`
	Title      string    `json:"title"`
	Status     string    `json:"status"`
	Error      string    `json:"error,omitempty"`
	GenVersion int       `json:"gen_version"`
	ItemCount  int       `json:"item_count"`
	Done       int       `json:"done"`  // leaves ticked, filled on read
	Leaves     int       `json:"leaves"` // total leaves, filled on read
	Version    int       `json:"version"`
	HLC        string    `json:"hlc"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	Items      []Item    `json:"items,omitempty"`
	Sources    []Source  `json:"sources,omitempty"`
}

type Item struct {
	ID        uuid.UUID  `json:"id"`
	RoadmapID uuid.UUID  `json:"roadmap_id"`
	ParentID  *uuid.UUID `json:"parent_id,omitempty"`
	Title     string     `json:"title"`
	Detail    string     `json:"detail,omitempty"`
	Depth     int        `json:"depth"`
	Sort      int        `json:"sort"`
	Kind      string     `json:"kind"` // topic | leaf
	State     string     `json:"state"` // todo | done
	Version   int        `json:"version"`
	HLC       string     `json:"hlc"`
}

type Source struct {
	Filename string `json:"filename"`
	Bytes    int64  `json:"bytes"`
}

// Upload is one file handed to Create, already in memory.
type Upload struct {
	Filename string
	Ext      string
	Data     []byte
}

type Service struct {
	pool     *pgxpool.Pool
	feed     *syncfeed.Feed
	anydoc   *anydoc.Converter
	apiKey   string
	model    string
	endpoint string
	http     *http.Client
}

func New(pool *pgxpool.Pool, feed *syncfeed.Feed, ad *anydoc.Converter, apiKey, model string) *Service {
	if model == "" {
		model = "deepseek/deepseek-v4-flash"
	}
	return &Service{
		pool: pool, feed: feed, anydoc: ad, apiKey: apiKey, model: model,
		endpoint: "https://openrouter.ai/api/v1/chat/completions",
		http:     &http.Client{Timeout: 120 * time.Second},
	}
}

func (s *Service) Enabled() bool { return s.anydoc.Enabled() && s.apiKey != "" }

// ------------------------------------------------------------------- reads

func (s *Service) List(ctx context.Context) ([]Roadmap, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT r.id, r.subject, r.title, r.status, r.error, r.gen_version,
		       r.item_count, r.version, r.hlc, r.created_at, r.updated_at,
		       coalesce(l.leaves,0), coalesce(l.done,0)
		  FROM roadmaps r
		  LEFT JOIN LATERAL (
		    SELECT count(*) FILTER (WHERE kind='leaf') AS leaves,
		           count(*) FILTER (WHERE kind='leaf' AND state='done') AS done
		      FROM roadmap_items i
		     WHERE i.roadmap_id = r.id AND i.deleted_at IS NULL
		  ) l ON true
		 WHERE r.deleted_at IS NULL
		 ORDER BY r.updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Roadmap{}
	for rows.Next() {
		var r Roadmap
		if err := rows.Scan(&r.ID, &r.Subject, &r.Title, &r.Status, &r.Error, &r.GenVersion,
			&r.ItemCount, &r.Version, &r.HLC, &r.CreatedAt, &r.UpdatedAt, &r.Leaves, &r.Done); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Service) Get(ctx context.Context, id uuid.UUID) (Roadmap, error) {
	var r Roadmap
	err := s.pool.QueryRow(ctx, `
		SELECT id, subject, title, status, error, gen_version, item_count,
		       version, hlc, created_at, updated_at
		  FROM roadmaps WHERE id=$1 AND deleted_at IS NULL`, id).
		Scan(&r.ID, &r.Subject, &r.Title, &r.Status, &r.Error, &r.GenVersion,
			&r.ItemCount, &r.Version, &r.HLC, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Roadmap{}, ErrNotFound
	}
	if err != nil {
		return Roadmap{}, err
	}
	items, err := s.itemsFor(ctx, id)
	if err != nil {
		return Roadmap{}, err
	}
	r.Items = items
	for _, it := range items {
		if it.Kind == "leaf" {
			r.Leaves++
			if it.State == "done" {
				r.Done++
			}
		}
	}
	rows, _ := s.pool.Query(ctx,
		`SELECT filename, bytes FROM roadmap_sources WHERE roadmap_id=$1 ORDER BY added_at`, id)
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var sc Source
			if err := rows.Scan(&sc.Filename, &sc.Bytes); err == nil {
				r.Sources = append(r.Sources, sc)
			}
		}
	}
	return r, nil
}

func (s *Service) itemsFor(ctx context.Context, id uuid.UUID) ([]Item, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, roadmap_id, parent_id, title, detail, depth, sort, kind, state, version, hlc
		  FROM roadmap_items
		 WHERE roadmap_id=$1 AND deleted_at IS NULL
		 ORDER BY sort`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Item{}
	for rows.Next() {
		var it Item
		if err := rows.Scan(&it.ID, &it.RoadmapID, &it.ParentID, &it.Title, &it.Detail,
			&it.Depth, &it.Sort, &it.Kind, &it.State, &it.Version, &it.HLC); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// ------------------------------------------------------------------ writes

// SetItemState ticks or unticks one leaf.
func (s *Service) SetItemState(ctx context.Context, device, itemID uuid.UUID, state string) (Item, error) {
	if state != "todo" && state != "done" {
		return Item{}, errors.New("bad state")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Item{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `
		UPDATE roadmap_items
		   SET state=$2,
		       completed_at = CASE WHEN $2='done' THEN coalesce(completed_at, now()) ELSE NULL END,
		       version=version+1, hlc=$3, updated_at=now()
		 WHERE id=$1 AND deleted_at IS NULL`, itemID, state, s.feed.Clock.Now())
	if err != nil {
		return Item{}, err
	}
	if tag.RowsAffected() == 0 {
		return Item{}, ErrNotFound
	}
	if _, err := s.feed.Append(ctx, tx, "roadmap_item", itemID, "upsert", device); err != nil {
		return Item{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Item{}, err
	}
	var it Item
	err = s.pool.QueryRow(ctx, `
		SELECT id, roadmap_id, parent_id, title, detail, depth, sort, kind, state, version, hlc
		  FROM roadmap_items WHERE id=$1`, itemID).
		Scan(&it.ID, &it.RoadmapID, &it.ParentID, &it.Title, &it.Detail,
			&it.Depth, &it.Sort, &it.Kind, &it.State, &it.Version, &it.HLC)
	return it, err
}

func (s *Service) Delete(ctx context.Context, device, id uuid.UUID) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx,
		`UPDATE roadmaps SET deleted_at=now(), hlc=$2, version=version+1 WHERE id=$1 AND deleted_at IS NULL`,
		id, s.feed.Clock.Now())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if _, err := s.feed.Append(ctx, tx, "roadmap", id, "delete", device); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
