-- In-app activity.
--
-- Web Push reaches a locked phone but leaves no trace: dismiss the banner, or
-- have the phone face-down while it buzzes, and the fact that a reminder fired
-- is gone. A push is a delivery mechanism, not a record.
--
-- So every push-worthy event also writes a row here. The two are deliberately
-- independent -- a notification still lands in the app when push is switched
-- off, when the browser never granted permission, or when the subscription has
-- quietly expired, which is the common failure and the one you cannot see.

CREATE TABLE notifications (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind       text NOT NULL CHECK (kind IN ('reminder', 'review', 'digest', 'job', 'system')),
    title      text NOT NULL,
    body       text NOT NULL DEFAULT '',
    -- Where tapping it goes. Stored rather than derived, so an old row still
    -- points somewhere sensible after the routes move.
    url        text NOT NULL DEFAULT '/',

    task_id    uuid REFERENCES tasks(id)  ON DELETE CASCADE,
    event_id   uuid REFERENCES events(id) ON DELETE CASCADE,

    -- Collapses repeats: a second reminder for the same task replaces the
    -- unread one rather than stacking a duplicate.
    dedupe_key text,

    read_at    timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- The badge count and the list are the only two reads, and both want unread
-- first, newest first.
CREATE INDEX notifications_unread ON notifications (created_at DESC) WHERE read_at IS NULL;
CREATE INDEX notifications_recent ON notifications (created_at DESC);
CREATE UNIQUE INDEX notifications_dedupe ON notifications (dedupe_key)
    WHERE dedupe_key IS NOT NULL AND read_at IS NULL;
