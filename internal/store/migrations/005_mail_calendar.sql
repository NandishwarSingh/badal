-- Mail ingestion and the calendar it feeds.

-- ------------------------------------------------------------- settings
-- Small key/value store for things the user toggles. Kept in Postgres rather
-- than a config file so the PWA can change them without a deploy.
CREATE TABLE app_settings (
    key        text PRIMARY KEY,
    value      jsonb       NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- Review-before-write is the default. Auto-confirm is opt-in, per the rule
-- that an unreviewed model write to your calendar destroys trust in it.
INSERT INTO app_settings (key, value) VALUES
    ('mail.auto_confirm',        'false'::jsonb),
    ('mail.auto_confirm_min',    '0.85'::jsonb),
    ('mail.backfill_days',       '7'::jsonb),
    ('mail.poll_seconds',        '120'::jsonb)
ON CONFLICT (key) DO NOTHING;

-- --------------------------------------------------------- mail accounts
CREATE TABLE mail_accounts (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email         text        NOT NULL UNIQUE,
    imap_host     text        NOT NULL,
    imap_port     integer     NOT NULL DEFAULT 993,
    username      text        NOT NULL,
    -- AES-GCM sealed with BADAL_SECRET_KEY. An app password is a live
    -- credential to the whole mailbox; it does not belong in plaintext even
    -- in a single-user database.
    password_enc  bytea       NOT NULL,
    -- IMAP UID cursor. uid_validity guards the case where the server renumbers
    -- everything: if it changes, every stored UID is meaningless and we resync.
    last_uid      bigint      NOT NULL DEFAULT 0,
    uid_validity  bigint,
    backfill_done boolean     NOT NULL DEFAULT false,
    enabled       boolean     NOT NULL DEFAULT true,
    last_sync_at  timestamptz,
    last_error    text,
    created_at    timestamptz NOT NULL DEFAULT now()
);

-- --------------------------------------------------------------- messages
CREATE TABLE messages (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id      uuid        NOT NULL REFERENCES mail_accounts(id) ON DELETE CASCADE,
    message_id      text        NOT NULL,          -- RFC 5322 Message-ID
    uid             bigint      NOT NULL,
    subject         text        NOT NULL DEFAULT '',
    from_name       text        NOT NULL DEFAULT '',
    from_addr       text        NOT NULL DEFAULT '',
    to_addrs        text[]      NOT NULL DEFAULT '{}',
    received_at     timestamptz NOT NULL,
    snippet         text        NOT NULL DEFAULT '',
    body_text       text        NOT NULL DEFAULT '',
    -- Object key of the untouched MIME source. Archived forever so extraction
    -- can be re-run with a better model without re-fetching anything.
    raw_key         text        NOT NULL,
    raw_size        integer     NOT NULL DEFAULT 0,
    attachments     jsonb       NOT NULL DEFAULT '[]',
    -- Which extractor version last ran. Bumping the prompt bumps this, which
    -- is what makes re-extraction produce a new version rather than duplicates.
    extraction_version integer,
    extracted_at    timestamptz,
    extract_error   text,
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (account_id, message_id)
);

CREATE INDEX messages_received  ON messages (received_at DESC);
CREATE INDEX messages_pending   ON messages (received_at) WHERE extracted_at IS NULL;
CREATE INDEX messages_account_uid ON messages (account_id, uid);

-- --------------------------------------------------------------- events
CREATE TABLE events (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    title        text        NOT NULL,
    starts_at    timestamptz NOT NULL,
    ends_at      timestamptz,
    all_day      boolean     NOT NULL DEFAULT false,
    location     text        NOT NULL DEFAULT '',
    kind         text        NOT NULL DEFAULT 'event'
                 CHECK (kind IN ('exam','deadline','class','event','meeting','task')),
    notes        text        NOT NULL DEFAULT '',
    -- proposed until a human says otherwise. See app_settings.mail.auto_confirm.
    state        text        NOT NULL DEFAULT 'proposed'
                 CHECK (state IN ('proposed','confirmed','rejected')),
    confidence   real        NOT NULL DEFAULT 0,
    -- Verbatim span from the source that justifies this date. The extractor is
    -- required to supply it and the server checks it really occurs in the
    -- message; that check is what rejects most hallucinated dates.
    evidence     text        NOT NULL DEFAULT '',
    source_message uuid REFERENCES messages(id) ON DELETE SET NULL,
    extraction_version integer,
    hlc          text        NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    deleted_at   timestamptz
);

CREATE INDEX events_range    ON events (starts_at) WHERE deleted_at IS NULL;
CREATE INDEX events_proposed ON events (created_at DESC) WHERE state = 'proposed' AND deleted_at IS NULL;

-- Dedupe key. Re-extracting the same message must update the row rather than
-- add a second copy of the same deadline.
CREATE UNIQUE INDEX events_dedupe
    ON events (source_message, lower(title), starts_at)
    WHERE source_message IS NOT NULL AND deleted_at IS NULL;
