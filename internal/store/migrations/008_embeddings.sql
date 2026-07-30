-- Retrieval index over everything: notes, mail, events, and later transcripts.
--
-- pgvector rather than SurrealDB. The searchable corpus is notes PLUS mail PLUS
-- events, and the latter two live only here -- vectors in Surreal would mean
-- duplicating every email body into it. Surreal keeps graph traversal, which is
-- what it is actually good at.
--
-- pgvector also keeps the index on disk under Postgres' buffer manager rather
-- than resident like Surreal's HNSW, which is what makes a large corpus
-- survivable on 4GB.

CREATE TABLE embeddings (
    id           bigserial PRIMARY KEY,
    -- Polymorphic on purpose: one index over every kind of content means one
    -- query answers "where did I see this", regardless of where it came from.
    source_type  text        NOT NULL CHECK (source_type IN ('note','message','event','transcript','image')),
    source_id    uuid        NOT NULL,
    chunk_index  integer     NOT NULL DEFAULT 0,

    -- The exact text that was embedded. Kept so results can be shown without a
    -- second lookup, and so re-chunking can be diffed against what exists.
    content      text        NOT NULL,
    -- Larger surrounding text handed to the model at generation time. Retrieval
    -- wants small chunks for precision; generation wants context. Storing both
    -- is the "small-to-big" pattern.
    parent       text        NOT NULL DEFAULT '',

    -- halfvec: 16-bit floats. Half the storage and faster distance maths with
    -- no measurable recall loss at 768 dimensions.
    embedding    halfvec(768) NOT NULL,

    -- Denormalised for pre-filtering. Cutting the candidate set before ANN
    -- beats making ANN cleverer.
    occurred_at  timestamptz,
    subjects     text[]      NOT NULL DEFAULT '{}',

    model        text        NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),

    UNIQUE (source_type, source_id, chunk_index)
);

-- One row per source so re-embedding after an edit is a delete-and-insert
-- rather than a scan.
CREATE INDEX embeddings_source ON embeddings (source_type, source_id);
CREATE INDEX embeddings_when   ON embeddings (occurred_at DESC);
CREATE INDEX embeddings_subj   ON embeddings USING GIN (subjects);

-- HNSW over cosine distance. m=16/ef_construction=64 are the defaults that
-- trade build time for recall sensibly; on one vCPU a heavier build would
-- block the API for minutes.
CREATE INDEX embeddings_hnsw ON embeddings
    USING hnsw (embedding halfvec_cosine_ops)
    WITH (m = 16, ef_construction = 64);

-- Tracks what still needs embedding, so a backfill can be resumed and an edit
-- can invalidate just its own rows.
CREATE TABLE embed_queue (
    source_type text        NOT NULL,
    source_id   uuid        NOT NULL,
    reason      text        NOT NULL DEFAULT 'new',
    queued_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (source_type, source_id)
);
