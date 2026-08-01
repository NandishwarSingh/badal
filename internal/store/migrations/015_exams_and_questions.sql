-- Exam dates from VTOP, and a question bank built out of past papers.
--
-- Two corrections to 014.
--
-- First, exam dates. 014 left them out on the grounds that events already held
-- CAT-I, extracted from a circular. That was wrong: a circular announces a
-- window, VTOP holds the schedule -- which course, which date, which session,
-- which venue, which seat. Those are different facts and only one of them can
-- be revised at short notice. Both are kept, and VTOP wins.
--
-- Second, practice. The point of holding papers is not to list them. Twenty-
-- five past CAT-1 papers exist for one course and several carry answer keys,
-- so the questions that actually get asked are already written down. Generated
-- questions are the fallback, never the default, and every row records which
-- it is.

CREATE TABLE exams (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    course_id   uuid REFERENCES courses(id) ON DELETE CASCADE,
    -- Kept even when no course matches, so an import is never silently partial.
    course_code text NOT NULL DEFAULT '',
    kind        text NOT NULL DEFAULT 'CAT-1'
                CHECK (kind IN ('CAT-1', 'CAT-2', 'FAT', 'Quiz', 'Lab', 'Other')),
    starts_at   timestamptz NOT NULL,
    ends_at     timestamptz,
    slot        text NOT NULL DEFAULT '',
    venue       text NOT NULL DEFAULT '',
    seat        text NOT NULL DEFAULT '',
    -- 'vtop' is authoritative. 'mail' rows come from extraction and are a
    -- warning that a date exists, not a claim about which one.
    source      text NOT NULL DEFAULT 'vtop' CHECK (source IN ('vtop', 'mail', 'manual')),
    -- The extracted event this corresponds to, when one does, so the calendar
    -- can show a single thing rather than two versions of it.
    event_id    uuid REFERENCES events(id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- One authoritative row per course and exam. A re-import updates rather than
-- duplicating, because VTOP reissues the schedule when a date moves.
CREATE UNIQUE INDEX exams_vtop_unique ON exams (lower(course_code), kind)
    WHERE source = 'vtop';
CREATE INDEX exams_upcoming ON exams (starts_at);

-- Questions, mostly lifted from real papers.
CREATE TABLE questions (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    paper_id   uuid REFERENCES papers(id) ON DELETE CASCADE,
    course_id  uuid REFERENCES courses(id) ON DELETE CASCADE,
    topic_id   uuid REFERENCES topics(id) ON DELETE SET NULL,

    number     text NOT NULL DEFAULT '',        -- "3 (b)"
    prompt     text NOT NULL,
    marks      integer NOT NULL DEFAULT 0,

    -- Where the answer came from matters more than whether one exists. A key
    -- printed on the paper is fact; a model's answer is a study aid that can
    -- be wrong, and the two must never look alike on screen.
    answer     text NOT NULL DEFAULT '',
    answer_src text NOT NULL DEFAULT 'none'
               CHECK (answer_src IN ('none', 'key', 'model')),
    -- 'paper' means this was really asked. 'model' means it was written to
    -- resemble one.
    origin     text NOT NULL DEFAULT 'paper' CHECK (origin IN ('paper', 'model')),

    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX questions_course ON questions (course_id, topic_id);
CREATE INDEX questions_paper ON questions (paper_id);
-- Real questions with a real key are the best practice material there is, so
-- they are the cheapest thing to find.
CREATE INDEX questions_gold ON questions (course_id)
    WHERE origin = 'paper' AND answer_src = 'key';

-- What you did with them. Drives which topics the planner pushes back at you.
CREATE TABLE attempts (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    question_id uuid NOT NULL REFERENCES questions(id) ON DELETE CASCADE,
    -- Self-marked. An automatic grade on a written maths answer would be
    -- confidently wrong, and being told you are right when you are not is the
    -- single worst thing a revision tool can do.
    verdict     text NOT NULL CHECK (verdict IN ('got_it', 'shaky', 'missed')),
    seconds     integer NOT NULL DEFAULT 0,
    note        text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX attempts_question ON attempts (question_id, created_at DESC);

-- Paper text, so questions can be pulled out of a PDF and the paper itself
-- becomes searchable alongside everything else.
ALTER TABLE papers
    ADD COLUMN text_content text NOT NULL DEFAULT '',
    ADD COLUMN extracted_at timestamptz;

-- Papers join the corpus like any other item.
ALTER TABLE embeddings DROP CONSTRAINT embeddings_source_type_check;
ALTER TABLE embeddings ADD CONSTRAINT embeddings_source_type_check
    CHECK (source_type IN ('note','message','event','transcript','image','task','paper'));
