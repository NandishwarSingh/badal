-- Passkeys and Web Push.
--
-- Device tokens (001) stay for the CLI and the worker, where a browser
-- credential makes no sense. Browsers use passkeys instead: nothing
-- long-lived is stored in localStorage, so an XSS cannot walk off with a
-- credential that still works tomorrow.

-- A passkey identifies the human, not a device -- one per phone, laptop, key.
CREATE TABLE webauthn_credentials (
    id              bytea PRIMARY KEY,          -- raw credential id from the authenticator
    public_key      bytea       NOT NULL,
    attestation     text        NOT NULL DEFAULT '',
    aaguid          bytea,
    -- Bumped by the authenticator on each use. A value that goes backwards
    -- means the credential was cloned, so we refuse it.
    sign_count      bigint      NOT NULL DEFAULT 0,
    transports      text[]      NOT NULL DEFAULT '{}',
    backup_eligible boolean     NOT NULL DEFAULT false,
    backup_state    boolean     NOT NULL DEFAULT false,
    label           text        NOT NULL DEFAULT 'passkey',
    created_at      timestamptz NOT NULL DEFAULT now(),
    last_used_at    timestamptz
);

-- Short-lived server-side challenge state. WebAuthn requires the challenge to
-- be issued and verified by us; keeping it in the DB means it survives a
-- restart mid-ceremony and cannot be replayed once consumed.
CREATE TABLE webauthn_sessions (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    purpose    text        NOT NULL CHECK (purpose IN ('register','login')),
    data       jsonb       NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX webauthn_sessions_expiry ON webauthn_sessions (expires_at);

-- Browser sessions minted after a successful passkey ceremony. The cookie
-- carries only the opaque token; the hash is what we store, same as devices.
CREATE TABLE sessions (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    token_hash    bytea       NOT NULL UNIQUE,
    device_id     uuid        REFERENCES devices(id) ON DELETE CASCADE,
    credential_id bytea       REFERENCES webauthn_credentials(id) ON DELETE SET NULL,
    user_agent    text        NOT NULL DEFAULT '',
    expires_at    timestamptz NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    last_seen_at  timestamptz
);
CREATE INDEX sessions_expiry ON sessions (expires_at);

-- Web Push endpoints. iOS only delivers to home-screen-installed PWAs, which
-- is why the UI pushes people to install before offering notifications.
CREATE TABLE push_subscriptions (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    endpoint   text        NOT NULL UNIQUE,
    p256dh     text        NOT NULL,
    auth       text        NOT NULL,
    device_id  uuid        REFERENCES devices(id) ON DELETE CASCADE,
    user_agent text        NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    -- A push service returning 404/410 means the subscription is dead for
    -- good; we record it rather than retrying forever.
    failed_at  timestamptz
);

-- Single-row store for the VAPID keypair, generated once on first boot. Keys
-- must be stable: rotating them invalidates every existing subscription.
CREATE TABLE push_config (
    id          integer PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    public_key  text NOT NULL,
    private_key text NOT NULL,
    subject     text NOT NULL DEFAULT 'mailto:nandubhai222@gmail.com',
    created_at  timestamptz NOT NULL DEFAULT now()
);
