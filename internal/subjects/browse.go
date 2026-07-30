package subjects

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Hit mirrors the search result shape so the app renders one kind of card
// whether the list came from a query or from tapping a subject.
type Hit struct {
	SourceType string     `json:"source_type"`
	SourceID   uuid.UUID  `json:"source_id"`
	Title      string     `json:"title"`
	Snippet    string     `json:"snippet"`
	OccurredAt *time.Time `json:"occurred_at,omitempty"`
	Slug       string     `json:"slug,omitempty"`
	Subjects   []string   `json:"subjects,omitempty"`
}

// Browse lists everything carrying any of the given subjects, newest first.
//
// This is what makes a subject chip worth tapping with no query typed: the
// filter is a way in, not just a way to narrow. Matching any rather than all
// keeps two chips a union, which is what a row of chips looks like it does.
func (s *Service) Browse(ctx context.Context, slugs, types []string, limit int) ([]Hit, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		WITH matched AS (
		  SELECT DISTINCT source_type, source_id
		    FROM source_subjects
		   WHERE ($1::text[] IS NULL OR slug = ANY($1))
		)
		SELECT m.source_type, m.source_id,
		       coalesce(n.title, ms.subject, ev.title, '') AS title,
		       left(coalesce(n.body, ms.body_text, ev.notes, ''), 240) AS snippet,
		       coalesce(n.updated_at, ms.received_at, ev.starts_at) AS at,
		       coalesce(n.slug, '') AS slug,
		       coalesce(sj.subs, '{}') AS subjects
		  FROM matched m
		  LEFT JOIN notes    n  ON m.source_type='note'    AND n.id  = m.source_id AND n.deleted_at IS NULL
		  LEFT JOIN messages ms ON m.source_type='message' AND ms.id = m.source_id
		  LEFT JOIN events   ev ON m.source_type='event'   AND ev.id = m.source_id AND ev.deleted_at IS NULL
		  LEFT JOIN LATERAL (
		    SELECT array_agg(ss.slug ORDER BY ss.confidence DESC) AS subs
		      FROM source_subjects ss
		     WHERE ss.source_type = m.source_type AND ss.source_id = m.source_id
		  ) sj ON true
		 WHERE ($2::text[] IS NULL OR m.source_type = ANY($2))
		   -- A deleted note leaves its tags behind; the join makes it a row of
		   -- nulls rather than dropping it, so it has to be excluded here.
		   AND coalesce(n.id, ms.id, ev.id) IS NOT NULL
		 ORDER BY at DESC NULLS LAST
		 LIMIT $3`, nilIfEmpty(slugs), nilIfEmpty(types), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Hit{}
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.SourceType, &h.SourceID, &h.Title, &h.Snippet,
			&h.OccurredAt, &h.Slug, &h.Subjects); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func nilIfEmpty(v []string) any {
	if len(v) == 0 {
		return nil
	}
	return v
}
