-- The academic calendar: the raw dates everything else hangs off.
--
-- Three sources now describe when things happen, and they are not rivals --
-- they answer different questions, and the planner needs all three:
--
--   academic calendar  the semester's shape. CAT-I runs 8-14 August, this
--                      Friday is a holiday, instruction ends on the 14th.
--                      Institute-wide, published months ahead, day-granular.
--
--   VTOP               your schedule inside that shape. Which course sits on
--                      which day, in which session, at which seat. Exact,
--                      personal, and revised at short notice.
--
--   mail               a circular saying a thing exists, extracted with the
--                      sentence that proved it. Earliest warning, least
--                      precise, and sometimes the only source there is.
--
-- Precedence when they disagree: VTOP, then the calendar, then mail. A
-- circular announcing "CAT-I from the 8th" must never overwrite a VTOP row
-- saying your paper is on the 11th at 2pm in SJT-204.
--
-- Dates, not timestamps. An academic calendar is published as days; storing
-- "8 August" as an instant invents a time nobody stated and shifts it the
-- moment a timezone is applied.

CREATE TABLE calendar_entries (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind       text NOT NULL CHECK (kind IN (
                   'semester',      -- the term's own start and end
                   'exam_window',   -- CAT-I runs these days
                   'holiday',       -- no instruction: a whole day back
                   'instructional', -- a working day, sometimes on a weekend
                   'event'          -- everything else worth seeing on a plan
               )),
    label      text NOT NULL,
    starts_on  date NOT NULL,
    -- Inclusive, and equal to starts_on for a single day, so every query is
    -- one BETWEEN rather than two cases.
    ends_on    date NOT NULL,
    -- Which exam window this is, when it is one, so an exam with no VTOP row
    -- can still be planned against the window it falls in.
    exam_kind  text CHECK (exam_kind IN ('CAT-1', 'CAT-2', 'FAT', 'Quiz', 'Lab')),
    -- A Monday timetable run on a Saturday to make up a lost day. VTOP does
    -- this several times a semester and it silently breaks any planner that
    -- assumes Saturday is free.
    day_order  smallint CHECK (day_order BETWEEN 1 AND 7),
    source     text NOT NULL DEFAULT 'calendar'
               CHECK (source IN ('calendar', 'vtop', 'mail', 'manual')),
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (ends_on >= starts_on)
);

CREATE INDEX calendar_span ON calendar_entries (starts_on, ends_on);
CREATE INDEX calendar_windows ON calendar_entries (exam_kind, starts_on)
    WHERE exam_kind IS NOT NULL;
-- Re-importing a calendar replaces rather than duplicates.
CREATE UNIQUE INDEX calendar_unique ON calendar_entries (source, lower(label), starts_on);

-- An exam can now name the window it belongs to, so a course with no VTOP row
-- yet is still planned against "CAT-I, sometime in these seven days" rather
-- than being left out of the plan entirely.
ALTER TABLE exams
    ADD COLUMN window_id uuid REFERENCES calendar_entries(id) ON DELETE SET NULL,
    -- False while only the window is known, true once VTOP gives a real slot.
    ADD COLUMN date_confirmed boolean NOT NULL DEFAULT false;
