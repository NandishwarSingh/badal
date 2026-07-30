# badal — Personal Cloud Platform

**Revision 2** · 2026-07-30 · built on Phase 0 as deployed

A self-hosted personal cloud: notes as a knowledge graph, files, email intelligence, and
calendar — across an always-on VPS and an intermittent Mac worker, delivered as one PWA.

---

## 1. Decisions of record

| Decision | Choice | Date |
|---|---|---|
| Clients | **PWA everywhere** — installed on iOS + Android, browser on macOS. No native apps | Jul 30 |
| Server language | Go (single static binary, low RAM) | Jul 29 |
| System of record | Postgres 17 + pgvector | Jul 29 |
| Knowledge graph | **SurrealDB** — notes graph, links, embeddings | Jul 30 |
| Blob store | Garage S3 on VPS (hot) → Mac bundle (archive) | Jul 30 |
| Eviction | **Enabled**, VPS → Mac, 90-day window | Jul 30 |
| Worker | **MacBook Air M5 only.** No cloud OCR/ASR | Jul 30 |
| Text extraction | OpenRouter `deepseek/deepseek-v4-flash` (text-only, $0.09/1M) | Jul 29 |
| VPS size | Hostinger KVM 1 — 1 vCPU / 4 GB / 50 GB, unchanged | Jul 30 |
| Mail | Google Workspace (`vitstudent.ac.in`) → Gmail API | Jul 29 |

### Hardware

| | badal-core (VPS) | badal-mac (worker + client) | badal-forge (shelved) |
|---|---|---|---|
| CPU | 1 vCPU | Apple M5, 10 core | Ryzen 5 4600H, 12 thread |
| RAM | 4 GB | 16 GB unified | 7.2 GB |
| Disk | 50 GB | 261 GB free | 80 GB root + 300 GB archive LV |
| GPU | — | M5 Neural Engine / Metal | GTX 1650 4 GB |
| Net | `100.73.64.48`, public 443 | `100.127.18.18` | `100.88.36.28`, offline |
| OS | CentOS Stream 9 | macOS | Fedora 44 |

Domain `nandishwarsingh.com` · timezone **Asia/Kolkata** · servers log in UTC.

---

## 2. Why PWA, and what it costs

One SvelteKit codebase serves the browser on macOS and installs to the home screen on iOS
and Android. This removes the Apple Developer account, the App Store, TestFlight, native
build tooling, and a whole second language from the project.

**What works on iOS PWAs** (16.4+, home-screen installed):

- **Web Push** — real push notifications, no APNs, no $99/yr
- Service worker + IndexedDB offline
- `getUserMedia` for audio recording, `<input capture>` for camera

**What does not, and the workaround:**

| Limitation | Workaround |
|---|---|
| No Background Sync API on Safari | Sync on app open + Web Push wake. Nothing syncs while closed |
| No Share Target on iOS (Android has it) | iOS Shortcut posting to the API; Share Target on Android |
| IndexedDB evicted after ~7 days unused | Home-screen install is far more persistent; server is the source of truth anyway |
| No Finder/File Provider integration | WebDAV endpoint that macOS can mount natively (§8) |

**The honest trade:** offline-while-closed sync is gone. For notes and capture — where you
open the app to use it — this barely matters. It would matter for a Dropbox-style always-
syncing folder, which is why §8 uses WebDAV rather than pretending a PWA can do it.

---

## 3. Storage and eviction

```
        upload
          │
          ▼
   ┌──────────────┐   replicate job    ┌────────────────────────┐
   │ VPS Garage   │ ─────────────────► │ Mac /badal-archive/    │
   │ badal-hot    │                    │   blobs/   ← archive   │
   │ 30 GB cap    │ ◄───── restore ─── │   restic/  ← backups   │
   └──────────────┘                    │ 150 GB sparse bundle   │
          │                            └────────────────────────┘
          └── evict at 90d, ONLY if verified present on Mac
```

### The eviction contract

Eviction is the only operation in badal that can destroy data. It runs as a job with four
preconditions, **all** required:

1. Object age > **90 days** (long, because the archive is intermittent)
2. `archived_at IS NOT NULL`
3. Live verification: `HEAD` on the Mac copy **and** BLAKE3 checksum match
4. Object is not `pinned`

Fail any one and the object stays. The flag is never trusted on its own — a stale
`archived_at` after a Mac disk failure would otherwise authorise deletion.

### Cold reads degrade, never fail

Requesting an evicted object while the Mac is offline returns **`202 Accepted`** with a
queued retrieval. The PWA shows *"restoring from archive"* and a Web Push fires when it
lands. This path is not an edge case here — the Mac is a laptop, so it is the normal case
for anything older than 90 days.

### ⚠️ Evicted blobs have exactly one copy

Once evicted, the Mac holds the only copy. Lose or wipe the Mac and that data is gone —
the restic replica backs up Postgres and config, **not** the blob archive.

Mitigations that cost nothing and are in the plan:

- **`pinned` flag** — mark anything irreplaceable and it never leaves the VPS.
- **90-day window** — recent data is always two-copy.
- The bundle is sparse: 150 GB provisioned, 48 MB actually consumed. Growing it later is
  one command.

The real fix remains a second archive: the shelved laptop's 300 GB LV (needs a cable) or
cloud object storage. Until then this is a known, accepted single point of failure.

---

## 4. Data stores and their boundaries

Three stateful services, each with a clear job. Boundaries matter because there are no
cross-store transactions.

| Store | Owns | Why |
|---|---|---|
| **Postgres 17** | `change_log`, devices/auth, jobs, email metadata, calendar events, blob manifests, object ACLs | System of record. Already backed up and restore-drilled. Transactional |
| **SurrealDB** | Note graph, note bodies, links/backlinks, vector embeddings, tags | Graph traversal + vector search in one engine, without Neo4j's JVM |
| **Garage** | Blob content, addressed by BLAKE3 chunk hash | S3 semantics, cheap, swappable |

**Postgres stays the source of truth for identity and ordering.** SurrealDB is the
knowledge layer. If they ever disagree, Postgres wins and Surreal is rebuilt from it —
that rule makes the lack of cross-store transactions survivable.

**Consequences to handle:**
- SurrealDB must join the backup path (`surreal export` into the restic staging dir).
  Non-negotiable — it will hold your notes.
- Memory on a 4 GB box: Postgres 512 MB + Surreal ~350 MB + Garage 150 MB + VM 100 MB +
  Caddy/ntfy ~50 MB ≈ **1.2 GB of 3.6 GB**. Comfortable, but this is the last stateful
  service that fits. Anything more forces the VPS upgrade deferred in §1.

### The sync spine (unchanged, already designed)

Every mutation appends to `change_log(seq, entity_type, entity_id, op, actor_device, hlc)`.
Clients hold a `since_seq` cursor and pull. The WebSocket is a **doorbell, not a data
channel** — it only says "new seq available". Reconnection, missed messages and cold start
all become the same code path.

---

## 5. Worker: badal-mac

`badal-forge` runs as a **launchd agent on the Mac**. The lease protocol from Revision 1 is
unchanged and is what makes a laptop viable as a worker:

- Claim job → 60 s lease → heartbeat every 20 s → VPS reaper requeues on expiry
- Agent holds a `caffeinate` assertion **for the duration of each job**, so closing the lid
  or unplugging mid-job cannot kill it
- `com.badal.power` daemon keeps the Mac awake on AC (lid closed and idle), and restores
  normal sleep on battery

**Mac-only compute means latency is a product decision, not a bug.** Capture a photo on
your phone while the Mac is shut and nothing processes until it wakes. Every queued job
therefore surfaces in the UI as *"queued — will process when your Mac is available"*, with
the queue visible. Being honest in the interface is the whole mitigation.

**The Air is fanless.** Sustained ASR/embedding runs with the lid closed will thermally
throttle — graceful, not damaging, but throughput drops. The agent caps concurrency at 2
jobs and backs off above 90 °C.

Email text extraction stays on **OpenRouter** — it is text-only, costs under $1/month, and
must work when the Mac is shut, since deadline extraction is time-sensitive.

---

## 6. Phase 7 — the knowledge layer (expanded)

This is the payoff phase and now the largest. Everything below runs on the Mac worker.

### 6.1 Image → structured note

```
PWA camera/upload → blob store → job(kind=image_note)
  → Apple Vision VNRecognizeTextRequest      (OCR + word-level bounding boxes)
  → layout reconstruction from boxes         (columns, tables, headings)
  → local VLM via MLX                        (diagrams, charts, scene context)
  → LLM structuring → markdown note + entities + suggested links
  → SurrealDB: note node + embedding + edges
```

Apple's Vision framework is the right primary: native, no model download, no Python, and
better on skewed phone photos than RapidOCR. The VLM only runs when OCR confidence is low
or the image is non-textual — most captures never need it.

### 6.2 Audio → structured note, without the hallucinations

Whisper hallucinates confidently on silence and noise — the notorious *"Thank you for
watching"* on empty audio. It is the single biggest quality problem in ASR pipelines, and
the fix is preprocessing, not prompting.

```
MediaRecorder (webm/opus) → blob → job(kind=audio_note)
  1. ffmpeg → 16 kHz mono PCM
  2. Silero VAD → speech segment timestamps
  3. DROP non-speech; concatenate speech only, keeping a map back to original offsets
  4. whisper.cpp (Metal) per segment
  5. post-filter: drop segments with no_speech_prob > 0.6, or matching a
     known-hallucination phrase list, or repeating n-grams (the loop failure mode)
  6. re-map timestamps to the original recording
  7. LLM structuring → title, summary, action items, entities
  8. SurrealDB note + embedding + edges
```

Steps 3 and 5 are the anti-hallucination core: **never send silence to the model**, and
distrust what it returns for low-confidence spans. Timestamp remapping (6) matters because
dropping silence shifts every offset — without it, playback sync is wrong.

### 6.3 Chat with your notes

Retrieval is hybrid, and the graph is what makes it better than plain RAG:

1. Vector search over note embeddings (SurrealDB)
2. Full-text/BM25 for exact terms — names and IDs that embeddings blur
3. **Graph expansion**: pull 1-hop neighbours of top hits. A note's linked notes are
   usually relevant context that pure vector search misses
4. Rerank, assemble, answer with **inline citations to note IDs**

Answers cite sources, always. An unsourced answer about your own notes is worse than none.

### 6.4 Search

One box, three signals blended: full-text, vector, graph proximity. Filters for date,
tag, source (note / email / image / audio), and entity.

---

## 7. Email → Calendar (unchanged design, revised sequencing)

Gmail API, `gmail.readonly`, incremental via stored `historyId`. Full pipeline in
Revision 1 and still current:

- Raw MIME archived immutably — re-extract later with a better model, never re-fetch
- Structured JSON output with a mandatory **`evidence`** field: a verbatim quote that must
  appear in the source, or the extraction is rejected. Kills most hallucinated dates
- Relative dates resolved against `received_at`, never `now()`
- Idempotent on `(message_id, extraction_version)`
- Events land as **`proposed`**, confirmed one-tap from a review inbox. Auto-writing
  low-confidence events makes the calendar untrustworthy within a month
- Signed `.ics` subscription URL — native Apple Calendar on Mac and iPhone, ~100 lines

**Sequencing note:** Phase 4 now runs *before* the worker (Phase 3). Attachment OCR
therefore ships in two parts — text attachments (`pdftotext` on the VPS) work immediately;
image and scanned attachments queue and process once Phase 3 lands.

---

## 8. Files, without a File Provider

The PWA cannot mount a drive in Finder. Instead the API exposes **WebDAV**, which macOS
mounts natively (`Finder → Go → Connect to Server`), and Windows/Linux support too.

Same chunked content-addressed store underneath (FastCDC + BLAKE3, probe-then-upload only
missing chunks). WebDAV is a second protocol over the same manifests, not a second store.

---

## 9. Operations (as built — Phase 0 complete)

### badal-core

| | |
|---|---|
| TLS | Caddy, Let's Encrypt, `badal.` + `ntfy.` subdomains |
| Containers | **podman quadlets** — systemd-native, `Restart=always`, no daemon |
| SSH | key-only, **tailnet-only** (port 22 removed from the public zone) |
| Firewall | public: `http https` · trusted: `tailscale0` |
| SELinux | **Enforcing**, 0 denials |
| Swap | 2 GB, `vm.swappiness=10` |
| Backups | restic nightly 02:30 UTC → dedicated Garage bucket + separate key |
| Restore | **verified** — 5 000 rows incl. `vector` restored, checksum identical. Monthly drill |
| Replica | nightly 21:30 to the Mac bundle, `restic check --read-data` clean |
| Metrics | VictoriaMetrics + node_exporter, tailnet-only, 6 month retention |
| Alerts | `badal-healthcheck` every 15 min → ntfy. Disk, backup age, failed units, containers, TLS expiry, memory |

### Hard-won operational knowledge

**SELinux `disabled` → `enforcing` directly will brick the box.** With no labels on disk,
the policy denies everything including the relabel itself. It deadlocks before journald
starts, so there is no log. Always go via `permissive` + `/.autorelabel`, verify 0 AVCs,
then `enforcing`. Recovery is `POST /virtual-machines/{id}/recovery` — an Ubuntu rescue OS
with the real disk as `/dev/sdb1`.

**`firewall-cmd --reload` silently breaks container networking.** It flushes netavark's
rules; containers stay healthy but stop resolving each other. Nothing logs an error.
`badal-fw-watch.service` watches the `Reloaded` D-Bus signal and runs `podman network
reload --all`. Note netavark uses the **iptables** backend here — `nft list tables` is not
a valid health check.

**sshd takes the first occurrence of a keyword.** Fedora ships
`01-permitrootlogin.conf` with `PermitRootLogin yes`, which beats any `99-` drop-in. Use a
`00-` prefix.

**A health check that exits non-zero marks its own systemd unit failed** — which the next
run then reports as a problem. Self-sustaining false alarm. Exit 0; the notification is
the signal.

**Hostinger `public-keys/attach` returns 200 but queues nothing** on a running VM — keys
are only baked in at reinstall. Use `PUT .../root-password` instead. Backups are
`GET`-only via API; manual snapshots need hPanel.

### badal-forge (shelved, recoverable)

On the tailnet, hardened, root grown to 80 GB, **300 GB `fedora/archive` LV formatted and
mounted**. Outstanding: nouveau owns the GPU and RPMFusion's blacklist was never
installed. Fix as one atomic operation — `/etc/modprobe.d` blacklist, `grubby
--update-kernel=ALL --args="rd.driver.blacklist=nouveau modprobe.blacklist=nouveau"`,
`dracut --regenerate-all`, reboot — and delete `/etc/modules-load.d/badal-nvidia.conf`.

Root cause of shelving: tethered to an iPhone hotspot, which powers down when the phone
locks. Four disconnections in an hour. The machine was never at fault. **One ethernet
cable brings back a 12-thread node and a 300 GB archive.**

---

## 10. Roadmap (your ordering)

| # | Phase | Est. | Depends on | Done when |
|---|---|---|---|---|
| **0** | Foundation | — | — | ✅ **complete** |
| **1** | **Spine** — Go API, auth/devices, `change_log`, FastCDC blobs, `badal` CLI | 3 wk | 0 | `badal push`/`pull` from the terminal |
| **2** | **Notes + PWA** — SurrealDB, note graph, SvelteKit PWA, offline, Web Push, quick capture | 5 wk | 1 | **You use it daily** |
| **3** | **Email → Calendar** — Gmail ingest, extraction + evidence validation, review inbox, `.ics` | 5 wk | 2 | Apple Calendar shows VIT deadlines you confirmed |
| **4** | **Worker** — job queue, lease/heartbeat/reaper, launchd agent, caffeinate, thermal backoff | 2 wk | 1 | Close the lid mid-job → it still completes |
| **5** | **Knowledge layer** — image→note, audio→note with VAD, chat-with-notes, hybrid search | 7 wk | 2, 4 | Photograph a whiteboard, get a linked structured note |
| **6** | **Files** — WebDAV, chunked sync, eviction to the Mac archive, cold-read restore | 4 wk | 1, 4 | Finder mounts badal; a 90-day-old file restores on demand |
| **7** | **Beyond** — §11 | — | — | — |

**Total to a genuinely useful system: ~22 weeks.** Phase 2 is the one that decides whether
this survives — if it is not in daily use by then, everything before it is plumbing you
cannot feel.

Phase 4 shrank from 3 weeks to 2: the systemd watchdogs, lid/power handling, WiFi recovery
and thermal caps all died with the laptop. launchd needs none of it.

---

## 11. Suggested next features, ranked by value-to-effort

1. **Daily digest** — one morning push: today's events, week's deadlines, mail needing
   action, notes you touched. Ties everything together and makes it feel like *yours*.
2. **Web clipper** — browser extension → page as markdown into the graph. Trivial once
   notes exist.
3. **Auto-linking** — on note save, embed and suggest the 5 nearest existing notes.
   Builds the graph without manual `[[links]]`, which nobody sustains.
4. **Entity extraction → graph nodes** — people, courses, deadlines become first-class
   nodes. "Show everything about Prof. X" starts working.
5. **Timeline view** — every note, email and capture on one scrubable axis.
6. **Templates + recurring notes** — daily journal, lecture notes, meeting notes.
7. **Public share links** — signed, expiring, single note or folder.
8. **Import** — Notion/Obsidian/Apple Notes ingestion, so the graph starts populated.
9. **Voice command capture** — hold a button in the PWA, speak, it becomes a routed note
   or a calendar event. Uses the whole Phase 5 pipeline.
10. **Local LLM option** — 16 GB unified runs 8–14B models. Route privacy-sensitive
    extraction to the Mac and keep OpenRouter for the rest.

Deliberately out of scope: media server (Jellyfin), password manager (Bitwarden), home
automation, multi-user. Each is its own project.

---

## 12. Open risks

| Risk | Status |
|---|---|
| Evicted blobs are single-copy on the Mac | **Accepted.** Mitigated by `pinned` + 90-day window |
| SurrealDB is the last service that fits in 4 GB | Monitored. Next stateful service forces a VPS upgrade |
| Mac-only compute means captures wait for the lid to open | **Accepted.** Surfaced honestly in the UI |
| iOS PWAs cannot sync while closed | **Accepted.** Sync on open + Web Push |
| Gmail tenant may block IMAP *and* third-party OAuth | **Unverified.** Check `myaccount.google.com/apppasswords` before Phase 3 |
| 30 GB blob ceiling until eviction is live | Resolved by Phase 6 |

---

## 16. Phase 5 expanded — the knowledge layer

Revised 2026-07-30. Everything needed is already installed: ffmpeg, whisper-cli,
ollama (`qwen3.5:4b`), pgvector 0.8.5, SurrealDB HNSW, Apple Vision, and the
job queue with a live Mac worker.

### 16.0 Retrieval lives in pgvector, not SurrealDB

Revision 2 put embeddings in SurrealDB. That is now wrong: the searchable corpus
is notes **plus mail plus events**, and mail and events exist only in Postgres.
Vectors in Surreal would mean projecting every email body into it, duplicating
content and doubling the backup surface for no gain.

| Store | Job |
|---|---|
| **pgvector** | One retrieval index over notes, mail, events, transcripts, OCR |
| **SurrealDB** | Graph traversal — neighbours, 1-hop expansion, entity edges |

pgvector also keeps the index on disk under Postgres' buffer management, rather
than resident like Surreal's HNSW. That is what makes a large corpus survivable
on 4 GB.

### 16.1 What "best in class" actually means here

In rough order of impact on answer quality:

1. **Cross-encoder reranking.** Retrieve 50, rerank, keep 8. The single biggest
   quality lever in RAG and the one most often skipped. `bge-reranker-v2-m3` on
   the Mac worker.
2. **Hybrid retrieval with RRF.** Dense misses exact tokens — course codes, room
   numbers, names. BM25 misses paraphrase. Reciprocal Rank Fusion over both.
3. **Query rewriting.** "What about that one" is not a retrieval query. Every
   conversational turn is rewritten to a standalone query before search.
4. **Small-to-big.** Embed small chunks for precision, then feed the *parent*
   section to the model for context. Retrieval and generation want different
   granularity.
5. **Structure-aware chunking.** Split on headings, list boundaries and email
   quote levels, with overlap — never fixed character counts.
6. **Metadata pre-filter.** Date range, source type, subject. Cutting the
   candidate set before ANN beats making ANN cleverer.

### 16.2 Scaling to a very large corpus

- **`halfvec(768)`** — 16-bit vectors halve storage and speed up distance
  computation with no measurable recall loss at this scale.
- **Binary quantization pre-pass** at very large scale: `bit(768)` Hamming
  search to shortlist thousands, then rerank with full vectors. pgvector
  supports both natively.
- **HNSW built incrementally**, not as one blocking build. With 1 vCPU and
  `maintenance_work_mem=128MB`, a single large build would stall the API.
- **Partial indexes per source type**, so a note-only search never traverses
  email vectors.

> ⚠️ The honest ceiling: embedding and reranking run on the Mac, but *query
> serving* is the VPS. One vCPU is what eventually limits concurrent search
> latency at very large corpus sizes. This is the workload that would justify
> the upgrade deferred in §1.

### 16.3 Conversation, not Q&A

Chat uses `deepseek/deepseek-v4-flash`, streamed. The target is sub-second to
first token and sub-second to first audio, because anything slower stops feeling
like talking.

```
mic → whisper-cli (Metal, chunked)      ~200ms
    → query rewrite + hybrid search     ~150ms
    → rerank on Mac                     ~120ms
    → DeepSeek V4 Flash, streamed       first token ~400ms
    → TTS, streamed sentence by sentence
```

**Voice out is two-tier.** macOS `say` is built in, 184 voices, effectively zero
latency — that ships first. **Fish Audio** (OpenAudio S1) is the quality
upgrade, but it needs PyTorch and weights in a separate Python 3.12 environment;
torch has no 3.14 wheels yet. It becomes an opt-in mode, not the default,
because a better voice that takes two seconds to start is worse than a plainer
one that starts instantly.

**Barge-in** is required: speaking must cut the TTS mid-sentence. Without it a
voice assistant is a monologue you wait out.

Conversation keeps turn history; retrieval does not. Each turn retrieves fresh
against the rewritten query, so the model cannot drift onto stale context.

### 16.4 Subjects

A subject taxonomy derived from the corpus itself rather than hand-maintained:
courses, clubs, recurring senders. Every note, mail and event gets auto-tagged.
Subjects then drive smart collections, filters and the graph.

### 16.5 Reminders

Distinct from calendar events: a reminder is a *notification at a time*, not a
block in a day.

- **Photograph anything with a date** — a poster, a whiteboard, a form — and it
  becomes a reminder with the source image attached.
- **Speak one** — "remind me to submit the form on Friday" — through the same
  audio path.
- **Fires as Web Push** at a lead time you choose, using the infrastructure
  already proven in Phase 3's digest.

Reminders created from a picture or voice go to review first, same rule as
calendar events, unless auto-confirm is on.

### 16.6 Sub-phases

| | Weeks | Scope |
|---|---|---|
| **5A · Retrieval** | 1–3 | pgvector schema, chunking, embeddings on the worker, hybrid + RRF, reranker, query rewriting, subjects |
| **5B · Conversation** | 4–5 | Streaming chat, voice in, voice out with `say`, barge-in, citations, Fish Audio as opt-in |
| **5C · Capture** | 6–8 | Audio→note with VAD, image→note, lecture mode, reminders from picture and voice |
| **5D · Graph** | 9–10 | Auto-linking, entities as nodes, related-on-open, smart collections |

Ten weeks rather than seven, because the expansion roughly doubles the scope.

### 16.7 The audio pipeline, unchanged

```
record → ffmpeg 16kHz mono
       → Silero VAD, drop non-speech ENTIRELY
       → whisper-cli (Metal) on speech only
       → filter: no_speech_prob > 0.6, repeating n-grams
       → remap timestamps to the original recording
       → structure → title, summary, actions, entities
```

Dropping silence is what stops Whisper hallucinating *"Thank you for watching"*
into a lecture. Remapping matters because removing silence shifts every offset —
skip it and playback sync is silently wrong.

`whisper-cli` is installed but **no model is cached**; `base.en` or `small.en`
needs pulling on first run.
