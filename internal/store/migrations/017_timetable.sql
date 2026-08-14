-- Timetable: a recurring weekly schedule of classes and study blocks.
--
-- Deliberately not events and not tasks. An event (005) is one-off, extracted
-- from mail, and dated with a verbatim justification. A task (011) is a
-- done/not-done intention. A timetable slot is neither: it recurs by weekday,
-- is fixed for a term, has no review workflow, and is answered by "what is on
-- right now" rather than "what did I finish". Forcing it through either table
-- would mean a nullable, meaningless column on every row of the other.
--
-- Time is stored as minutes-from-midnight in the timetable's own timezone, not
-- as a timestamp: the slot recurs every week, so a wall-clock offset is the
-- honest representation and needs no per-occurrence date arithmetic. A block
-- that runs past midnight (a 21:00-01:00 study session) is encoded with
-- ends_min > 1440 -- 01:00 is 1500 -- so it stays a single contiguous row.

CREATE TABLE timetables (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text        NOT NULL,
    tz         text        NOT NULL DEFAULT 'Asia/Kolkata',
    -- One timetable is the current one. Switching terms flips this rather than
    -- deleting last term's rows, so history survives.
    active     boolean     NOT NULL DEFAULT true,
    version    integer     NOT NULL DEFAULT 1,
    hlc        text        NOT NULL DEFAULT '',
    deleted_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
-- At most one active, non-deleted timetable.
CREATE UNIQUE INDEX timetables_one_active
    ON timetables ((true)) WHERE active AND deleted_at IS NULL;

CREATE TABLE timetable_slots (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    timetable_id uuid NOT NULL REFERENCES timetables(id) ON DELETE CASCADE,
    kind         text        NOT NULL DEFAULT 'class'
                             CHECK (kind IN ('class','study')),
    title        text        NOT NULL,
    course_code  text        NOT NULL DEFAULT '',
    location     text        NOT NULL DEFAULT '',
    day_of_week  smallint    NOT NULL CHECK (day_of_week BETWEEN 0 AND 6), -- 0=Mon
    starts_min   smallint    NOT NULL CHECK (starts_min >= 0 AND starts_min < 1440),
    -- Up to 1560 (26:00) so a block ending at 01:00/02:00 next day is one row.
    ends_min     smallint    NOT NULL CHECK (ends_min > 0 AND ends_min <= 1560),
    notes        text        NOT NULL DEFAULT '',
    version      integer     NOT NULL DEFAULT 1,      -- optimistic concurrency, as tasks
    hlc          text        NOT NULL,
    deleted_at   timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    CHECK (ends_min > starts_min)
);
-- The only read: a term's live slots, ordered as the day runs.
CREATE INDEX timetable_slots_live
    ON timetable_slots (timetable_id, day_of_week, starts_min)
    WHERE deleted_at IS NULL;

-- Seed the owner's FALL 2026 timetable (classes + the study plan). The seed
-- hlc sorts before any real clock value, so a later edit on any device always
-- wins over the seeded row.
WITH t AS (
    INSERT INTO timetables (name, tz, active, hlc)
    VALUES ('FALL 2026', 'Asia/Kolkata', true, '0000000000000.00000.seed')
    RETURNING id
)
INSERT INTO timetable_slots
    (timetable_id, day_of_week, kind, title, course_code, location, starts_min, ends_min, hlc)
SELECT t.id, v.dow, v.kind, v.title, v.code, v.loc, v.s, v.e, '0000000000000.00000.seed'
FROM t, (VALUES
  (0,'class','A1 · PAMAT501','PAMAT501','',480,530),
  (0,'class','F1 · PAMCA501','PAMCA501','',540,590),
  (0,'study','DSA','','campus',590,660),
  (0,'class','TB1 · PAMCA503','PAMCA503','',660,710),
  (0,'class','TG1 · PASTS601','PASTS601','',720,770),
  (0,'study','DSA','','campus',770,820),
  (0,'study','Probability & Stats','','campus',820,940),
  (0,'class','L33–L34 · PAMCA501 lab','PAMCA501','',951,1050),
  (0,'study','Java','','',1080,1200),
  (0,'study','Machine Learning','','',1200,1440),
  (1,'class','B1 · PAMCA503','PAMCA503','',480,530),
  (1,'class','G1 · PASTS601','PASTS601','',540,590),
  (1,'class','E1 · PAMCA502','PAMCA502','',600,650),
  (1,'class','TC1 · PAMCA601','PAMCA601','',660,710),
  (1,'class','TAA1 · PAMAT501','PAMAT501','',720,770),
  (1,'study','DSA','','campus',770,840),
  (1,'class','L37–L38 · PAENG501 lab','PAENG501','',840,940),
  (1,'study','DSA','','',970,1020),
  (1,'study','Probability & Stats','','',1020,1140),
  (1,'study','Java','','',1140,1260),
  (1,'study','Full Stack Dev','','',1260,1500),
  (2,'class','C1 · PAMCA601','PAMCA601','',480,530),
  (2,'class','A1 · PAMAT501','PAMAT501','',540,590),
  (2,'class','F1 · PAMCA501','PAMCA501','',600,650),
  (2,'study','Probability & Stats','','campus',650,770),
  (2,'study','DSA','','campus',770,840),
  (2,'class','L43–L44 · PAMCA503 lab','PAMCA503','',840,940),
  (2,'study','DSA','','',970,1020),
  (2,'study','Java','','',1020,1140),
  (2,'study','Machine Learning','','',1140,1380),
  (3,'study','Probability & Stats','','room',480,510),
  (3,'class','B1 · PAMCA503','PAMCA503','',540,590),
  (3,'class','G1 · PASTS601','PASTS601','',600,650),
  (3,'class','TE1 · PAMCA502','PAMCA502','',660,710),
  (3,'class','TCC1 · PAMCA601','PAMCA601','',720,770),
  (3,'study','Probability & Stats','','campus',770,860),
  (3,'study','Java','','campus',860,980),
  (3,'study','DSA','','campus',980,1040),
  (3,'class','L53–L54 · PAENG501 lab','PAENG501','',1060,1160),
  (3,'study','DSA','','',1190,1250),
  (3,'study','Full Stack Dev','','',1250,1490),
  (4,'class','E1 · PAMCA502','PAMCA502','',480,530),
  (4,'class','C1 · PAMCA601','PAMCA601','',540,590),
  (4,'class','TA1 · PAMAT501','PAMAT501','',600,650),
  (4,'class','TF1 · PAMCA501','PAMCA501','',660,710),
  (4,'class','TD1 · PAENG501','PAENG501','',720,770),
  (4,'study','Probability & Stats','','campus',770,890),
  (4,'study','DSA','','campus',890,950),
  (4,'class','L57–L58 · PAMCA502 lab','PAMCA502','',951,1050),
  (4,'study','DSA','','',1080,1140),
  (4,'study','Java','','',1140,1260),
  (4,'study','System Design','','',1260,1500),
  (5,'study','Technical Report Writing','','',540,780),
  (5,'study','Cloud Computing','','',840,1080),
  (5,'study','Research paper / own tech','','',1140,1260),
  (6,'study','DBMS','','',540,780),
  (6,'study','Competitive Coding','','',840,1080),
  (6,'study','Research paper / own tech','','',1140,1260)
) AS v(dow, kind, title, code, loc, s, e);
