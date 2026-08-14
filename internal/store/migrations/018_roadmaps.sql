-- Roadmaps: a subject's syllabus turned into a checkable tree.
--
-- One roadmap per subject. You upload the topic PDFs, anydoc converts each to
-- Markdown, and a coverage-checked LLM pass turns the merged headings into a
-- hierarchy of topics and leaf items you tick off. The "nothing missing" rule
-- is why generation records a version and re-runs replace rather than append.
--
-- Not tasks and not notes: a roadmap item has a parent, a depth and a done
-- flag, and its whole point is the tree. Tasks are flat and dated; forcing a
-- hierarchy through them would mean a parent column that means nothing to the
-- other 99% of rows.

CREATE TABLE roadmaps (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    subject     text        NOT NULL,           -- subject slug, or a free-form name
    title       text        NOT NULL,
    status      text        NOT NULL DEFAULT 'processing'
                            CHECK (status IN ('processing','ready','failed')),
    error       text        NOT NULL DEFAULT '',   -- why it failed, shown to the user
    gen_version integer     NOT NULL DEFAULT 0,    -- generator version baked in at build
    item_count  integer     NOT NULL DEFAULT 0,
    version     integer     NOT NULL DEFAULT 1,    -- optimistic concurrency, as tasks
    hlc         text        NOT NULL,
    deleted_at  timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX roadmaps_live ON roadmaps (updated_at DESC) WHERE deleted_at IS NULL;

CREATE TABLE roadmap_items (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    roadmap_id  uuid NOT NULL REFERENCES roadmaps(id) ON DELETE CASCADE,
    -- Self-reference builds the tree. A top-level unit has parent NULL.
    parent_id   uuid REFERENCES roadmap_items(id) ON DELETE CASCADE,
    title       text        NOT NULL,
    detail      text        NOT NULL DEFAULT '',   -- optional one-line gloss
    depth       smallint    NOT NULL DEFAULT 0,
    sort        integer     NOT NULL DEFAULT 0,    -- order within a parent
    -- A 'topic' groups; a 'leaf' is the thing you actually tick.
    kind        text        NOT NULL DEFAULT 'leaf' CHECK (kind IN ('topic','leaf')),
    state       text        NOT NULL DEFAULT 'todo' CHECK (state IN ('todo','done')),
    completed_at timestamptz,
    version     integer     NOT NULL DEFAULT 1,
    hlc         text        NOT NULL,
    deleted_at  timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX roadmap_items_tree ON roadmap_items (roadmap_id, sort)
    WHERE deleted_at IS NULL;
CREATE INDEX roadmap_items_parent ON roadmap_items (parent_id) WHERE deleted_at IS NULL;

-- The source documents behind a roadmap, so re-generation knows what to re-read
-- and the UI can show "built from 3 PDFs". Blobs are content-addressed already.
CREATE TABLE roadmap_sources (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    roadmap_id  uuid NOT NULL REFERENCES roadmaps(id) ON DELETE CASCADE,
    filename    text        NOT NULL,
    blob_hash   bytea,                 -- null while only the upload name is known
    bytes       bigint      NOT NULL DEFAULT 0,
    added_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX roadmap_sources_roadmap ON roadmap_sources (roadmap_id);
