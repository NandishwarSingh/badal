-- Inline conflicts.
--
-- The first design saved a losing edit as a whole second note (notes.conflict_of).
-- That is safe -- nothing is lost -- but it scatters the problem: you end up
-- with two notes in the list and no way to see what actually differs.
--
-- A conflict now attaches to the note it belongs to, holding just the rejected
-- version. The client renders a line diff against the current text and you
-- choose per resolution. The note list stays clean.

CREATE TABLE note_conflicts (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    note_id        uuid        NOT NULL REFERENCES notes(id) ON DELETE CASCADE,
    -- The edit that lost, exactly as submitted.
    incoming_title text        NOT NULL,
    incoming_body  text        NOT NULL,
    incoming_tags  text[]      NOT NULL DEFAULT '{}',
    -- Which version the loser was editing, and what it had become by the time
    -- the write arrived. Enough to explain "you edited v3, it is now v5".
    base_version   integer     NOT NULL,
    server_version integer     NOT NULL,
    actor_device   uuid        REFERENCES devices(id) ON DELETE SET NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    resolved_at    timestamptz,
    resolution     text CHECK (resolution IN ('current', 'incoming', 'custom'))
);

CREATE INDEX note_conflicts_open ON note_conflicts (note_id) WHERE resolved_at IS NULL;
CREATE INDEX note_conflicts_recent ON note_conflicts (created_at DESC) WHERE resolved_at IS NULL;

-- notes.conflict_of stays for the rows the old scheme already created; nothing
-- new writes to it. Those can be deleted by hand once merged.
COMMENT ON COLUMN notes.conflict_of IS
    'Legacy: conflict copies created before 004. New conflicts use note_conflicts.';
