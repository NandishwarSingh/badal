-- Exam preparation: what you take, when you are free, what you must learn.
--
-- Exam dates are deliberately absent from this file. They already exist, in
-- events, extracted from mail with the verbatim span that justified them --
-- CAT-I on 8 August came from a circular, not from anything typed in. The
-- planner reads those rather than asking you to enter them a second time.

CREATE TABLE courses (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    code       text NOT NULL,                    -- BMAT202L
    title      text NOT NULL,
    slot       text NOT NULL DEFAULT '',         -- B1, TB1, L39+L40
    kind       text NOT NULL DEFAULT 'theory' CHECK (kind IN ('theory', 'lab', 'project')),
    faculty    text NOT NULL DEFAULT '',
    venue      text NOT NULL DEFAULT '',
    credits    real NOT NULL DEFAULT 0,
    -- Which of the eighteen subjects this maps to, so a course inherits the
    -- tagging everything else already has.
    subject    text REFERENCES subjects(slug) ON DELETE SET NULL,
    archived   boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX courses_code_live ON courses (lower(code)) WHERE NOT archived;

-- One row per class in the week. Local wall-clock, not timestamps: a timetable
-- repeats weekly and has no date, and storing 09:00 as an instant would drift
-- the moment anything about the calendar changed.
CREATE TABLE class_times (
    id        uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    course_id uuid NOT NULL REFERENCES courses(id) ON DELETE CASCADE,
    weekday   smallint NOT NULL CHECK (weekday BETWEEN 1 AND 7), -- ISO: Monday = 1
    starts    time NOT NULL,
    ends      time NOT NULL,
    venue     text NOT NULL DEFAULT '',
    CHECK (ends > starts)
);

CREATE INDEX class_times_day ON class_times (weekday, starts);

-- The syllabus, one row per thing that could be examined.
CREATE TABLE topics (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    course_id  uuid NOT NULL REFERENCES courses(id) ON DELETE CASCADE,
    module     text NOT NULL DEFAULT '',
    title      text NOT NULL,
    position   integer NOT NULL DEFAULT 0,
    -- 0 unseen, 1 shaky, 2 getting there, 3 solid, 4 could teach it.
    --
    -- Self-assessed rather than inferred. A model guessing how well you know
    -- something from how long you looked at it would be confidently wrong, and
    -- the whole plan is weighted by this number.
    confidence smallint NOT NULL DEFAULT 0 CHECK (confidence BETWEEN 0 AND 4),
    -- Roughly how long a first pass takes. Used for planning, corrected by use.
    minutes    integer NOT NULL DEFAULT 45 CHECK (minutes BETWEEN 5 AND 600),
    last_studied timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX topics_course ON topics (course_id, position);
CREATE INDEX topics_weak ON topics (confidence) WHERE confidence <= 2;

-- Past papers. Metadata from the source, the PDF itself in blob storage, so a
-- paper survives the site going down and can be read on a train.
CREATE TABLE papers (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    source     text NOT NULL CHECK (source IN ('codechef', 'vitpapers')),
    -- Stable per source: the codechef row id, or the repo path. This is what
    -- makes re-importing idempotent.
    source_key text NOT NULL,
    course_code text NOT NULL DEFAULT '',
    subject_name text NOT NULL DEFAULT '',
    year       text NOT NULL DEFAULT '',
    exam       text NOT NULL DEFAULT '',        -- CAT-1, CAT-2, FAT
    slot       text NOT NULL DEFAULT '',
    campus     text NOT NULL DEFAULT '',
    has_key    boolean NOT NULL DEFAULT false,
    remote_url text NOT NULL,
    -- Path in blob storage once fetched; empty until then.
    blob_path  text NOT NULL DEFAULT '',
    bytes      bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX papers_source_key ON papers (source, source_key);
CREATE INDEX papers_lookup ON papers (course_code, exam, year DESC);

-- Generated study blocks. Regenerated freely, so anything you did is recorded
-- on the block itself rather than being inferred from a plan that has moved.
CREATE TABLE study_blocks (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    course_id  uuid REFERENCES courses(id) ON DELETE CASCADE,
    topic_id   uuid REFERENCES topics(id) ON DELETE SET NULL,
    starts_at  timestamptz NOT NULL,
    ends_at    timestamptz NOT NULL,
    kind       text NOT NULL DEFAULT 'study'
               CHECK (kind IN ('study', 'revise', 'practice', 'paper', 'lab', 'assignment', 'break')),
    state      text NOT NULL DEFAULT 'planned'
               CHECK (state IN ('planned', 'done', 'skipped')),
    -- Why this block exists, in words. A plan you cannot interrogate is a plan
    -- you stop trusting the first time it looks wrong.
    reason     text NOT NULL DEFAULT '',
    -- Blocks from the same generation share a run, so replanning can replace
    -- exactly what it produced and leave anything you finished alone.
    plan_run   uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (ends_at > starts_at)
);

CREATE INDEX study_blocks_when ON study_blocks (starts_at) WHERE state = 'planned';
CREATE INDEX study_blocks_run ON study_blocks (plan_run);

-- Commute, sleep and the floor under everything that is not revision. Kept as
-- settings rows rather than columns so the planner can gain a constraint
-- without a migration.
INSERT INTO app_settings (key, value) VALUES
    ('study.commute_minutes', '20'),
    ('study.day_starts', '"08:00"'),
    ('study.day_ends', '"23:00"'),
    ('study.block_minutes', '45'),
    ('study.break_minutes', '10'),
    ('study.max_daily_minutes', '240'),
    ('study.protect_evenings_from', '"21:00"')
ON CONFLICT (key) DO NOTHING;
