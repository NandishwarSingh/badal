-- badal notes: the knowledge graph's source of truth.
--
-- Note bodies live HERE, not in SurrealDB. Surreal is a derived index for
-- graph traversal and (later) vector search -- rebuildable from these tables.
-- That is what makes two stores without distributed transactions survivable:
-- if they ever disagree, Postgres wins and Surreal is reprojected.

CREATE TABLE notes (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    slug        text        NOT NULL,           -- stable key that [[wiki-links]] resolve against
    title       text        NOT NULL,
    body        text        NOT NULL DEFAULT '',
    tags        text[]      NOT NULL DEFAULT '{}',
    version     integer     NOT NULL DEFAULT 1, -- optimistic concurrency; see conflict_of
    hlc         text        NOT NULL,
    -- Set on a conflict copy. Offline edits on two devices produce a second
    -- note rather than silently discarding one side -- you can see both and
    -- merge by hand, which beats trusting a merge you cannot inspect.
    conflict_of uuid        REFERENCES notes(id) ON DELETE SET NULL,
    deleted_at  timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    search      tsvector GENERATED ALWAYS AS (
                    setweight(to_tsvector('english', coalesce(title, '')), 'A') ||
                    setweight(to_tsvector('english', coalesce(body,  '')), 'B')
                ) STORED
);

CREATE UNIQUE INDEX notes_slug_live ON notes (slug) WHERE deleted_at IS NULL;
CREATE INDEX notes_search  ON notes USING GIN (search);
CREATE INDEX notes_tags    ON notes USING GIN (tags);
CREATE INDEX notes_updated ON notes (updated_at DESC) WHERE deleted_at IS NULL;

-- Links parsed out of [[wiki-links]] in the body.
--
-- dst_slug is stored even when no such note exists yet. A dangling link is a
-- feature: writing [[reading list]] before creating that note is how the graph
-- actually gets built, and the edge resolves the moment the target appears.
CREATE TABLE note_links (
    src_id   uuid NOT NULL REFERENCES notes(id) ON DELETE CASCADE,
    dst_slug text NOT NULL,
    dst_id   uuid REFERENCES notes(id) ON DELETE SET NULL,
    PRIMARY KEY (src_id, dst_slug)
);
CREATE INDEX note_links_dst_slug ON note_links (dst_slug);
CREATE INDEX note_links_dst_id   ON note_links (dst_id) WHERE dst_id IS NOT NULL;

-- Where the SurrealDB projector has got to in the change feed. Keeping the
-- cursor in Postgres means a Surreal wipe cannot lose our place -- reset this
-- to 0 and the whole graph reprojects.
CREATE TABLE projector_state (
    name       text PRIMARY KEY,
    last_seq   bigint      NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO projector_state (name, last_seq) VALUES ('surreal', 0)
    ON CONFLICT (name) DO NOTHING;
