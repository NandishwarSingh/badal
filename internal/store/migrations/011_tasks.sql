-- Tasks and reminders.
--
-- Not a reuse of events. An event is extracted from mail, always dated, and
-- carries the verbatim span that justifies it -- events.starts_at is NOT NULL
-- for that reason. A task is something you intend to do, usually with no date
-- at all, and its lifecycle is done/not-done rather than proposed/confirmed.
-- Forcing both through one table would mean nullable starts_at and a state
-- column meaning two different things.
--
-- They cross-reference instead: a task can point at the event, note or message
-- it came from, so provenance survives without conflating the two.

CREATE TABLE tasks (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    title        text        NOT NULL,
    notes        text        NOT NULL DEFAULT '',   -- markdown, same renderer as a note
    state        text        NOT NULL DEFAULT 'open'
                             CHECK (state IN ('open', 'done', 'dropped')),
    priority     smallint    NOT NULL DEFAULT 0 CHECK (priority BETWEEN 0 AND 3),

    -- Undated is the common case, so this is nullable and everything that
    -- orders by it has to cope with NULL.
    due_at       timestamptz,
    all_day      boolean     NOT NULL DEFAULT true,

    -- Provenance. A task made from a mail deadline keeps the trail back.
    note_id      uuid REFERENCES notes(id)    ON DELETE SET NULL,
    event_id     uuid REFERENCES events(id)   ON DELETE SET NULL,
    message_id   uuid REFERENCES messages(id) ON DELETE SET NULL,

    completed_at timestamptz,
    version      integer     NOT NULL DEFAULT 1,   -- optimistic concurrency, as notes
    hlc          text        NOT NULL,
    deleted_at   timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

-- The home screen is "what needs me", so the ordering it uses is the index.
CREATE INDEX tasks_open_due ON tasks (due_at NULLS LAST, priority DESC)
    WHERE deleted_at IS NULL AND state = 'open';
CREATE INDEX tasks_updated ON tasks (updated_at DESC) WHERE deleted_at IS NULL;

-- Reminders are their own rows, not a column on the task.
--
-- "A day before and again an hour before" is the normal case, and each needs
-- its own fired/not-fired state. A timestamptz[] column could hold the times
-- but not the delivery state, so a restart would either re-send every reminder
-- or drop the ones in flight.
CREATE TABLE task_reminders (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    task_id    uuid NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    fire_at    timestamptz NOT NULL,
    fired_at   timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- The scheduler's only query: what is due and not yet sent. Partial, because
-- fired reminders accumulate forever and are never looked at again.
CREATE INDEX task_reminders_due ON task_reminders (fire_at)
    WHERE fired_at IS NULL;
CREATE INDEX task_reminders_task ON task_reminders (task_id);

-- Tasks join the corpus: searchable, and taggable with the same subjects as
-- everything else. Both enums are CHECK constraints, so widening them means
-- replacing the constraint.
ALTER TABLE embeddings      DROP CONSTRAINT embeddings_source_type_check;
ALTER TABLE embeddings      ADD  CONSTRAINT embeddings_source_type_check
    CHECK (source_type IN ('note','message','event','transcript','image','task'));

ALTER TABLE source_subjects DROP CONSTRAINT source_subjects_source_type_check;
ALTER TABLE source_subjects ADD  CONSTRAINT source_subjects_source_type_check
    CHECK (source_type IN ('note','message','event','task'));
