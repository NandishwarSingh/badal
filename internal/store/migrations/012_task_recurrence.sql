-- Repeating tasks.
--
-- Two columns rather than an RRULE. "Every 2 weeks" covers what a personal
-- to-do list actually repeats on; iCalendar's grammar exists to express things
-- like "the last weekday before the 15th", and carrying a parser for that
-- would be more code than the rest of tasks put together.
--
-- Completing a repeat does not rewrite the task in place. It closes this one
-- and inserts the next, so the Done list shows what you actually finished
-- rather than a single row that has silently moved eleven times.

ALTER TABLE tasks
    ADD COLUMN recur_every    text CHECK (recur_every IN ('day', 'week', 'month', 'year')),
    ADD COLUMN recur_interval smallint NOT NULL DEFAULT 1
        CHECK (recur_interval BETWEEN 1 AND 365),
    -- The occurrence this one was generated from. Purely a trail: nothing
    -- reads it yet, but without it a repeating task's history is unlinkable.
    ADD COLUMN recur_from     uuid REFERENCES tasks(id) ON DELETE SET NULL;

-- A repeat with no due date has nothing to advance from, so it is not a repeat.
ALTER TABLE tasks
    ADD CONSTRAINT tasks_recur_needs_due
    CHECK (recur_every IS NULL OR due_at IS NOT NULL);
