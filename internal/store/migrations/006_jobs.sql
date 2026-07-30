-- Job queue for the Mac worker.
--
-- Leases rather than locks. A worker that vanishes mid-job -- lid shut, wifi
-- gone, process killed -- must not strand the work, and it cannot be relied on
-- to release anything on its way out. So the claim expires on its own and a
-- reaper puts the job back.
--
-- Postgres rather than a broker: this is a single-user system, SKIP LOCKED
-- does everything needed, and one less daemon on a 1 vCPU box matters more
-- than throughput we will never approach.

CREATE TABLE jobs (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind         text        NOT NULL,          -- ocr | embed | transcribe | thumbnail
    payload      jsonb       NOT NULL DEFAULT '{}',
    state        text        NOT NULL DEFAULT 'queued'
                 CHECK (state IN ('queued','leased','done','failed','abandoned')),
    priority     integer     NOT NULL DEFAULT 5, -- lower runs first

    -- Lease. lease_expires_at is the only thing that matters for recovery:
    -- the worker heartbeats to extend it, and the reaper requeues anything
    -- that has stopped heartbeating.
    lease_owner      text,
    lease_expires_at timestamptz,

    attempts     integer     NOT NULL DEFAULT 0,
    max_attempts integer     NOT NULL DEFAULT 3,

    result       jsonb,
    error        text,

    -- Set when the caller gave up waiting and used the cloud instead. Kept
    -- rather than deleted so it is visible how often the Mac was unavailable.
    superseded_at timestamptz,

    created_at   timestamptz NOT NULL DEFAULT now(),
    started_at   timestamptz,
    finished_at  timestamptz
);

-- The claim query: queued work, best priority first, oldest first.
CREATE INDEX jobs_claimable ON jobs (priority, created_at)
    WHERE state = 'queued';

-- The reaper query: leases that have run out.
CREATE INDEX jobs_expired ON jobs (lease_expires_at)
    WHERE state = 'leased';

CREATE INDEX jobs_recent ON jobs (created_at DESC);

-- One OCR job per image per message. Re-running extraction should reuse the
-- text the worker already produced rather than paying for it twice.
CREATE UNIQUE INDEX jobs_dedupe ON jobs (kind, (payload->>'key'))
    WHERE payload ? 'key' AND state <> 'failed';
