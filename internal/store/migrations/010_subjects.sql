-- Subjects: a controlled taxonomy for auto-tagging.
--
-- Controlled rather than free-form. A model asked to invent its own tags
-- returns "Internship", "internships" and "Internship Opportunity" for three
-- copies of the same circular, and a filter built on that is noise dressed as
-- structure. The vocabulary lives here, and the tagger may only choose from it
-- because the slugs become an enum in its JSON schema -- invalid tags are
-- impossible rather than merely discouraged.

CREATE TABLE subjects (
    slug       text PRIMARY KEY,
    label      text NOT NULL,
    kind       text NOT NULL DEFAULT 'topic' CHECK (kind IN ('topic', 'course', 'org')),
    sort       integer NOT NULL DEFAULT 100,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- Seeded from what this corpus actually contains. Ordered by how often it is
-- likely to be reached for, not alphabetically: the filter row is horizontally
-- scrolled on a phone and the first few are the ones that must be visible
-- without scrolling.
INSERT INTO subjects (slug, label, sort) VALUES
    ('academics',        'Academics',        10),
    ('exams',            'Exams',            20),
    ('assignments',      'Assignments',      30),
    ('internships',      'Internships',      40),
    ('placements',       'Placements',       50),
    ('events',           'Events & Fests',   60),
    ('clubs',            'Clubs',            70),
    ('workshops',        'Workshops',        80),
    ('research',         'Research',         90),
    ('hostel',           'Hostel & Mess',   100),
    ('campus',           'Campus',          110),
    ('admin',            'Administration',  120),
    ('scholarships',     'Scholarships',    130),
    ('entrepreneurship', 'Startups',        140),
    ('sports',           'Sports',          150),
    ('health',           'Health & Safety', 160),
    ('travel',           'Travel & Leave',  170),
    ('personal',         'Personal',        180);

CREATE TABLE source_subjects (
    source_type text NOT NULL CHECK (source_type IN ('note', 'message', 'event')),
    source_id   uuid NOT NULL,
    slug        text NOT NULL REFERENCES subjects(slug) ON DELETE CASCADE,
    confidence  real NOT NULL DEFAULT 1,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (source_type, source_id, slug)
);

-- Browsing is "everything tagged X, newest first", so the slug leads.
CREATE INDEX source_subjects_slug ON source_subjects (slug, source_type);

-- One row per source the tagger has looked at, whatever it decided.
--
-- Without this, "untagged" and "tagged with nothing" are the same state, and a
-- source the model legitimately declines to tag comes back as pending on every
-- pass -- a backfill loop that never terminates. Recording the attempt is what
-- makes the anti-join honest.
CREATE TABLE subject_runs (
    source_type text NOT NULL,
    source_id   uuid NOT NULL,
    version     integer NOT NULL,
    model       text NOT NULL,
    tagged_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (source_type, source_id)
);
