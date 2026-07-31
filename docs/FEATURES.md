# badal — the feature list

One list, built one at a time, in this order. Each row is a thing you can use
when it lands, and each names how *you* check it rather than how I check it.

Sizes: **S** half a day · **M** a day · **L** two to three days.

`docs/PLAN.md` keeps the architecture and the hard-won operational knowledge.
`docs/DESIGN.md` decides where a feature goes in the interface. This file is
the running order.

---

## Built

| Feature | Where it lives |
|---|---|
| VPS, Tailscale, Caddy, podman quadlets, backups, SELinux | `deploy/` |
| Go API, device tokens, passkeys, `change_log` sync spine | `cmd/badal-api`, `internal/auth` |
| Content-defined chunking + BLAKE3 blobs on Garage S3 | `internal/blobs`, `internal/chunks` |
| Notes with `[[wiki links]]`, offline PWA, outbox, Web Push | `web/`, `internal/notes` |
| Inline conflict diff, and one-tap resolve-all on a note | `web/src/lib/ConflictCard.svelte` |
| Mail ingest (IMAP, 7-day backfill), MIME and PDF text | `internal/mail` |
| Mail → calendar events with verbatim evidence, review queue | `internal/extract` |
| Mac worker: lease queue, heartbeat, reaper, Apple Vision OCR | `cmd/badal-forge`, `internal/jobs` |
| Semantic search, hybrid RRF, reranking on by default | `internal/embed` |
| Structure-aware chunking, small-to-big parents | `internal/embed/chunk.go` |
| Eval harness — recall@k, MRR, latency on two query sets | `cmd/badal-eval`, `eval/` |
| Subjects: 18-topic taxonomy, auto-tagged, filter and browse | `internal/subjects` |
| Markdown notes with maths, flowcharts, tables, images | `web/src/lib/markdown.js` |
| Tasks and reminders, push with Done/Snooze, **Now** as home | `internal/tasks` |

---

## Next

### Finish tasks

| # | Feature | You verify | Size |
|---|---|---|---|
| 1 | **Mail deadline → task**, one tap from Review | A deadline in Review becomes a task that keeps a link back to the mail | S |
| 2 | **Repeating tasks** — daily, weekly, monthly, every N | A weekly task reappears after you complete it | M |
| 3 | **Tasks in search and the digest** | Search finds a task by its text; the 07:00 push lists what is due today | S |

### One corpus

| # | Feature | You verify | Size |
|---|---|---|---|
| 4 | **Library** — merge Search and Notes into one surface with type chips | Notes, mail, events and tasks in one list; filter by type and subject | M |
| 5 | **Calendar into Now** — month grid as a segment, retire the Calendar tab | The month view is a segment of Now; mail moved to Library | M |
| 6 | **Capture sheet** — the ⊕ offers Note or Task | ⊕ asks what you are capturing instead of assuming a note | S |

### Talk to it

| # | Feature | You verify | Size |
|---|---|---|---|
| 7 | **Streaming chat with citations** | Ask "when is the open project due" and get an answer with a tappable source | M |
| 8 | **Query rewriting + turn memory** | "And who do I email about it" works as a follow-up | S |
| 9 | **Voice in** — hold to speak, Whisper on the Mac | Speak a question, watch it transcribe and answer | M |
| 10 | **Voice out + barge-in** — `say` first | It talks back, and speaking over it cuts it off | M |
| 11 | **Fish Audio voice** on the Mac, opt-in | Better timbre; you decide if the latency is worth it | L |

### Capture anything

| # | Feature | You verify | Size |
|---|---|---|---|
| 12 | **PWA recorder** — record and upload | Record 30 seconds on your phone, see it appear | M |
| 13 | **VAD + Whisper on the worker** | A recording with long silences transcribes without invented text | L |
| 14 | **Transcript → structured note** | Title, summary and action items, not a wall of text | M |
| 15 | **Image → structured note** | Photograph a whiteboard, get usable markdown | M |
| 16 | **Reminder from a photo** | Photograph a poster, get a reminder with the image attached | M |
| 17 | **Reminder by voice** | "Remind me to submit the form Friday" | S |
| 18 | **Lecture mode** | A 90-minute recording becomes sectioned notes with jumpable timestamps | L |

### Connect it up

| # | Feature | You verify | Size |
|---|---|---|---|
| 19 | **Auto-linking on save** | Writing a note surfaces five genuinely related ones | M |
| 20 | **Related-on-open** | Opening anything shows what it sits near | S |
| 21 | **Entities as graph nodes** | "Everything about Prof X" spans mail, events, tasks and notes | L |
| 22 | **Smart collections** — a saved filter over subjects | "Internship deadlines" stays populated as mail arrives | M |
| 23 | **Timeline** — everything on one scrubable axis | Scroll back to March and see what you were doing | M |

### Files

| # | Feature | You verify | Size |
|---|---|---|---|
| 24 | **File browser** — upload, list, download | Upload from your phone, download on the Mac | M |
| 25 | **Photos** — thumbnails, grid, albums | A photo grid that stays smooth on a phone | M |
| 26 | **Organise** — folders, tags, search over file contents | Find a file by name, tag or what is inside it | M |
| 27 | **WebDAV** | Finder mounts badal as a drive | M |
| 28 | **Sharing** — signed, expiring links | Send someone a link that dies on schedule | M |
| 29 | **Eviction + cold restore** | A 90-day-old file restores from the Mac archive on demand | L |

### Later

| # | Feature | You verify | Size |
|---|---|---|---|
| 30 | **Web clipper** — browser extension, page to markdown | Clip a page, it lands as a note with its source | M |
| 31 | **Templates** — daily journal, lecture, meeting | Start a note from a template in two taps | S |
| 32 | **Import** — Notion, Obsidian, Apple Notes | The graph starts populated rather than empty | L |
| 33 | **Local LLM option** — route private extraction to the Mac | Extraction works with the OpenRouter key removed | L |

---

## Yours, not mine

| | Item | Why it is stuck |
|---|---|---|
| R1 | Install the power daemon | One `sudo` command. Mentioned several times; the Mac still sleeps on battery |
| R2 | Decide the second blob copy — wire the laptop, or a cloud archive | **Blocks #29.** Evicted blobs would have exactly one copy until this is settled |
| R3 | Revive badal-forge over ethernet — optional | Unlocks the 300GB archive |
| R4 | Review the extracted events and tell me the wrong ones | I cannot tune the extraction prompt against data only you can judge |

I also need a Whisper model (`small.en`, ~500MB) pulled to the Mac before #13.
That one is mine to do.

---

## Order, and how much it matters

1–3 finish what is half-built; leaving tasks partly done is worse than not
having started, because the home screen is the one screen you always see.

4–6 are the design guide's argument made real, and 4 is worth doing before the
list grows: it removes a tab and mostly deletes code.

7 is the largest single jump in what the system is worth to you, and everything
it needs already exists — retrieval, subjects, sections to cite.

12–18 need the Mac awake and a Whisper model, so R1 gates the useful half.

24–29 are the only group that can wait indefinitely without the rest feeling
unfinished. 29 must not ship before R2.
