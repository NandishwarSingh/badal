-- Worker presence.
--
-- Derived presence did not work: jobs.lease_owner is cleared on completion, so
-- a worker that is up and idle looks identical to one that has never existed.
-- The UI has to distinguish "your Mac is asleep" from "nothing to do", so
-- presence is recorded explicitly on every claim, including empty ones.
CREATE TABLE workers (
    owner       text PRIMARY KEY,
    kinds       text[]      NOT NULL DEFAULT '{}',
    last_seen   timestamptz NOT NULL DEFAULT now(),
    claims      bigint      NOT NULL DEFAULT 0,
    first_seen  timestamptz NOT NULL DEFAULT now()
);
