# badal

My personal cloud. Notes, files, mail, calendar and tasks in one offline-first
PWA, served by a Go API on a 1 vCPU / 4 GB VPS. My Mac picks up the heavy jobs
(OCR, speech-to-text, voice) when it's awake.

It runs at [badal.nandishwarsingh.com](https://badal.nandishwarsingh.com).
Login is by passkey and it's a single-user system, so there's no demo account.

## What it does

- Notes with `[[wiki links]]`, Markdown, maths and Mermaid, editable offline
- Files split into content-defined chunks (FastCDC) and stored by BLAKE3 hash in
  Garage, so a re-upload or a duplicate only sends the chunks that changed
- Mail over IMAP. An LLM pulls deadlines and events out of mail into the
  calendar, each one with the exact sentence it came from, and they wait in a
  review queue until I accept them
- Tasks and reminders with repeats, delivered as Web Push with Done and Snooze
- Search and chat over all of it, with citations back to the source
- OCR (Apple Vision), Whisper transcription and Kokoro voice on the Mac

## Architecture

```
  PWA (SvelteKit, IndexedDB cache + outbox)
          |
          | HTTPS
          v
  Caddy --> badal-api (Go, one binary)
               |-- Postgres 17 + pgvector   change log, notes, tasks, mail, events,
               |                            jobs, embeddings, devices
               |-- SurrealDB                note graph (links and backlinks),
               |                            rebuilt from Postgres if they disagree
               '-- Garage (S3)              file chunks by BLAKE3 hash
                      ^
  Mac worker (badal-forge) --- leases jobs over Tailscale: OCR, Whisper, TTS
```

Postgres is the source of truth. There are no transactions across stores, so
the rule is that SurrealDB can always be rebuilt from Postgres.

| Path | What |
|---|---|
| `cmd/badal-api` | HTTP API, auth, sync feed, job queue |
| `cmd/badal-forge` | the Mac worker |
| `cmd/badal-eval` | retrieval eval (below) |
| `internal/syncfeed` | change log and hybrid logical clock |
| `internal/notes`, `internal/tasks` | version-checked writes |
| `internal/chunks`, `internal/blobs` | FastCDC + BLAKE3, chunk manifests |
| `internal/jobs` | lease queue and reaper |
| `internal/embed`, `internal/chat` | chunking, hybrid search, rerank, chat |
| `internal/mail`, `internal/extract` | IMAP ingest, mail to calendar |
| `web/` | the PWA |

## Sync and conflicts

Every write appends a row to `change_log` (seq, entity, id, op, device, HLC).
Clients keep a `since_seq` cursor and pull from it. The feed carries ids, not
content, and the client fetches whatever changed. So reconnecting, missing an
update and starting cold are all the same code path.

The HLC (hybrid logical clock) is there because wall time isn't enough. Two
devices with skewed clocks, or a clock that jumps backwards, would otherwise
let an older write win.

Offline, the PWA writes to IndexedDB and queues the write in an outbox. Safari
has no Background Sync, so the client drains the outbox and pulls the feed when
the app opens or comes back into view, when the network returns, after each
local write, and every 60 seconds while it's open.

Notes are version-checked. A write says which version it was based on, and the
server locks the row (`SELECT ... FOR UPDATE`) before comparing. If the version
moved, the incoming edit isn't dropped. It's stored in `note_conflicts` with
both version numbers, and the client gets a 409. The client then removes that
outbox entry (retrying would just record the same conflict again) and shows a
conflict card with an inline diff. You keep your version, take the other one or
merge by hand, one conflict at a time or all at once.

Tasks are version-checked too, but last write wins. A title and a due date
have nothing to merge by hand, so the check is only there to catch a stale
client.

## Jobs on a laptop

The worker is a MacBook, so it can vanish mid-job. A claim is a 90-second
lease, the worker heartbeats to extend it, and a reaper on the server requeues
anything whose lease ran out (it checks every 20 seconds). Close the lid mid-job
and the work is back in the queue within two minutes. Anything queued while the
Mac is asleep shows in the app as waiting for the Mac.

## Retrieval and how it was measured

Search fuses Postgres full-text and pgvector results with reciprocal rank
fusion, then reranks. Notes and mail are chunked by structure into small chunks
for matching, and each match is scored with its wider parent section.

`cmd/badal-eval` runs query sets from `eval/` against the API. Ground truth is
regexes over result titles, so it survives re-indexing and new mail. There are
two sets: 18 queries where the title gives the answer away, and 16 harder ones
that ask for a fact buried mid-document, worded differently from the document.

| | normal R@1 / MRR | hard R@1 / MRR |
|---|---|---|
| before | 78% / 0.843 | 69% / 0.794 |
| small chunks + fusion only | 67% / 0.796 | 62% / 0.750 |
| shipped: small-to-big + rerank | 89% / 0.944 | 94% / 0.969 |

Small chunks alone made things worse. They only paid off once the reranker
scored each candidate with its parent section instead of the whole source
document. The rerank step costs about 500 ms at p50 (roughly 450 ms to 950 ms)
and falls back to the fused order if it fails.

Two caveats. The corpus is my own mail and notes, so you can't rerun these
numbers from the repo alone; the harness and queries are here, the data isn't.
And the sets are small: one query moves R@1 by about 6 points.

The full write-up, including why reranking was turned off once and why the
HNSW index was never being used, is in `docs/PLAN.md` section 17.

## Running it

You need Go 1.26, Postgres 17 with pgvector, an S3 store (Garage here),
SurrealDB, Node with pnpm, and an OpenRouter key for embeddings, chat and
extraction.

```sh
go build -o badal-api ./cmd/badal-api
export BADAL_DATABASE_URL=postgres://...
export BADAL_S3_KEY_ID=... BADAL_S3_SECRET=...   # endpoint defaults to 127.0.0.1:3900
export BADAL_SURREAL_URL=... BADAL_SURREAL_PASS=...
export BADAL_RPID=example.com BADAL_ORIGIN=https://example.com   # passkeys
export BADAL_OPENROUTER_KEY=...
./badal-api        # listens on :8080 and applies migrations on start

cd web && pnpm install && pnpm build   # static build, served by Caddy
```

`deploy/caddy/Caddyfile` shows how the API and the static app are served.
Migrations live in `internal/store/migrations` and are applied in filename
order.

## Tests

`go test ./...` covers retrieval chunking (`internal/embed`) and route
patterns (`internal/httpx`). `cd web && pnpm test` runs 44 checks on the
Markdown renderer, the conflict diff and the speech helpers. The server's sync and
conflict paths don't have automated tests yet.

## Docs

- `docs/PLAN.md`: architecture, decisions and operations notes
- `docs/FEATURES.md`: what's built and what's next
- `docs/DESIGN.md`: where features go in the interface

Built July to August 2026, and it has run on my VPS since.
