-- badal spine: devices, the change feed, and content-addressed blobs.

CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- ---------------------------------------------------------------- devices
-- Every client (CLI, PWA, worker) registers as a device with its own
-- revocable token. The worker gets narrow scopes so a compromised agent
-- cannot read mail.
CREATE TABLE devices (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name        text        NOT NULL,
    platform    text        NOT NULL CHECK (platform IN ('cli','web','ios','android','macos','worker')),
    token_hash  bytea       NOT NULL UNIQUE,      -- sha256(opaque token); plaintext never stored
    scopes      text[]      NOT NULL DEFAULT '{full}',
    created_at  timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz,
    revoked_at  timestamptz
);
CREATE INDEX devices_active ON devices (token_hash) WHERE revoked_at IS NULL;

-- ------------------------------------------------------------ change feed
-- The sync backbone. Every mutation appends here; clients hold a `seq`
-- cursor and pull. The websocket only rings a doorbell -- it carries no data,
-- so reconnect, missed messages and cold start are one code path.
CREATE TABLE change_log (
    seq          bigserial PRIMARY KEY,
    entity_type  text        NOT NULL,
    entity_id    uuid        NOT NULL,
    op           text        NOT NULL CHECK (op IN ('upsert','delete')),
    actor_device uuid        REFERENCES devices(id) ON DELETE SET NULL,
    hlc          text        NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX change_log_entity ON change_log (entity_type, entity_id);

-- ----------------------------------------------------------------- chunks
-- FastCDC content-defined chunks, addressed by BLAKE3. refcount lets us
-- garbage-collect only chunks no blob references any more.
CREATE TABLE chunks (
    hash       bytea PRIMARY KEY,
    size       integer     NOT NULL CHECK (size > 0),
    refcount   integer     NOT NULL DEFAULT 0 CHECK (refcount >= 0),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX chunks_unreferenced ON chunks (created_at) WHERE refcount = 0;

-- ------------------------------------------------------------------ blobs
-- A blob is an ordered list of chunk hashes. Dedup and delta-sync both fall
-- out of that for free.
--   pinned      -> never a candidate for eviction (see PLAN.md s3)
--   archived_at -> verified present on the Mac archive
--   evicted_at  -> content removed from hot storage; fetch from archive
CREATE TABLE blobs (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    hash         bytea       NOT NULL UNIQUE,     -- blake3 of full content
    size         bigint      NOT NULL CHECK (size >= 0),
    chunk_hashes bytea[]     NOT NULL,
    content_type text        NOT NULL DEFAULT 'application/octet-stream',
    pinned       boolean     NOT NULL DEFAULT false,
    archived_at  timestamptz,
    evicted_at   timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now()
);
-- eviction candidates: old, archived, not pinned, still hot
CREATE INDEX blobs_evictable ON blobs (created_at)
    WHERE evicted_at IS NULL AND archived_at IS NOT NULL AND pinned = false;

-- ------------------------------------------------------------------ files
-- Human-facing names over blobs. Soft-deleted so the change feed can carry
-- the tombstone to every client.
CREATE TABLE files (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    path       text        NOT NULL,
    blob_id    uuid        NOT NULL REFERENCES blobs(id),
    size       bigint      NOT NULL,
    version    integer     NOT NULL DEFAULT 1,
    hlc        text        NOT NULL,
    deleted_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX files_path_live ON files (path) WHERE deleted_at IS NULL;
