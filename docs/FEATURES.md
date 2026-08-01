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
| Tasks and reminders, repeats, push with Done/Snooze, **Now** as home | `internal/tasks` |
| In-app activity feed with unread count, beside Web Push | `internal/notify` |
| Design system: Manrope, no gradients, nothing boxed, one icon set | `web/src/app.css`, `Icon.svelte` |
| Dimension-aware image skeletons; uploads resized to 2048px WebP | `web/src/lib/attach.js` |
| Streaming chat with citations, query expansion, turn memory | `internal/chat`, `web/src/lib/Ask.svelte` |
| Voice in — hold to speak, Whisper on the Mac, nothing leaves it | `cmd/badal-forge/transcribe.go`, `Mic.svelte` |
| Voice out — Kokoro over a websocket relay, browser fallback, barge-in | `internal/voice`, `deploy/mac/badal-tts.py`, `speech.svelte.js` |

---

## Next

### One corpus

| # | Feature | You verify | Size |
|---|---|---|---|
| 4 | **Library** — merge Search and Notes into one surface with type chips | Notes, mail, events and tasks in one list; filter by type and subject | M |
| 5 | **Calendar into Now** — month grid as a segment, retire the Calendar tab | The month view is a segment of Now; mail moved to Library | M |
| 6 | ✅ **Capture chooser** — Note, Task or Photo | ⊕ asks what you are capturing instead of assuming a note | S |

### Talk to it

| # | Feature | You verify | Size |
|---|---|---|---|
| 7 | ✅ **Streaming chat with citations** | Ask "when is the open project due" and get an answer with a tappable source | M |
| 8 | ✅ **Query rewriting + turn memory** | "And who do I email about it" works as a follow-up | S |
| 9 | ✅ **Voice in** — hold to speak, Whisper on the Mac | Speak a question, watch it transcribe and answer | M |
| 10 | ✅ **Voice out + barge-in** — Kokoro on the Mac, browser fallback | It talks back, and grabbing the mic cuts it off | M |
| 11 | ~~Fish Audio~~ — superseded by Kokoro in #10 | — | — |

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

### Exam prep

Added 1 Aug 2026, out of band: CAT-I is on the 8th. badal already holds the
exam dates it extracted from mail, the tasks, the subjects and the search, so
this is a planner over things that mostly exist rather than a new product.

| # | Feature | You verify | Size |
|---|---|---|---|
| E1a | ✅ **Paper discovery** — both sources behind one endpoint | 73 papers for BMAT202L, 25 of them CAT-1 | S |
| E1b | **Paper text** — cache the PDFs, extract text, index them | A paper opens offline and turns up in search | M |
| E2a | **Academic calendar** — semester shape, exam windows, holidays, rescheduled day-orders | A holiday shows as a free day; a Saturday running Monday's timetable is not treated as free | S |
| E2b | **VTOP import** — a bookmarklet posts timetable *and* the exam schedule | Your week appears, and each exam has its real date, session, venue and seat | M |
| E3 | **Syllabus** — modules and topics per course, each with a confidence you set | Mark a module shaky and it gets more time | M |
| E6 | **Question bank** — pull individual questions out of the papers, pair them with the answer keys | "Show me every CAT-1 question on Bayes" returns real ones, with real answers | L |
| E7 | **Teach me** — explain a topic from the syllabus and the papers, with citations | Ask about a shaky topic and get an explanation grounded in what is actually examined | M |
| E8 | **Practice** — serve questions, you self-mark, wrong ones come back | A session that gets harder where you are weak | M |
| E4 | **The plan** — study blocks in real free time, weighted by exam distance and confidence | A schedule that fits between classes and leaves evenings alone | L |
| E5 | **Progress + balance** — a floor under labs, assignments and time off | It refuses to fill every hour, and says what it protected | M |

Two things learned after the first pass:

**Three sources of dates, and they are not rivals.** The academic calendar
gives the semester's shape — CAT-I runs 8–14 August, this Friday is a holiday,
Saturday runs a Monday timetable. VTOP gives your schedule inside that shape:
which course, which session, which seat. Mail gives the earliest warning and
the least precision.

Precedence when they disagree is **VTOP, then the calendar, then mail**. A
circular saying "CAT-I from the 8th" must never overwrite a VTOP row saying
your paper is on the 11th at 2pm in SJT-204. An exam with no VTOP row yet is
still planned against its window rather than dropped, and carries
`date_confirmed = false` until the real slot lands.

**Real questions beat generated ones.** Twenty-five past CAT-1 papers exist for
one course and several carry answer keys, so the questions that actually get
asked are already written down. Every question records whether it came from a
paper or a model, and whether its answer came from a printed key or a model --
because being told you are right when you are not is the worst thing a revision
tool can do.

Why a bookmarklet and not a login: VTOP has a captcha and short sessions, and
reading cookies out of the browser store means touching the Keychain and every
cookie for every site to get one timetable. A bookmarklet uses the session
already in the tab, holds no password, and keeps working when the login
changes.

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

Settled 1 Aug 2026:

| | Item | Outcome |
|---|---|---|
| R4 | Review the extracted events | **Closed.** Good enough as they are; the extraction prompt stays |
| R3 | Revive badal-forge over ethernet | **Dropped.** No plans, so the 300GB LV is not coming back |
| R2 | Second blob copy | **Cloud archive.** With the laptop dropped it is the only remaining option. Needs a provider picked and a card — see #29 |

Still open:

| | Item | What it costs you to leave |
|---|---|---|
| R1 | Install the power daemon | Not installed as of this check: no LaunchDaemon and `disablesleep` unset. The worker holds `caffeinate` per job, so anything already running finishes — but with the lid shut the Mac sleeps between jobs, so captures queue until you open it. One `sudo` |

~~Mine before #13: pull Whisper `small.en`.~~ **Done** — 466MB at
`~/.badal/models/ggml-small.en.bin`, Metal-accelerated, about 2.5s for a
short clip.

---

## Ranked

Two scores out of five. **Solves** is how much a real gap in badal closes —
something that is annoying, missing or breaking today. **Cool** is how much it
makes the thing feel like yours rather than a CRUD app. They are genuinely
different axes: eviction scores 4 and 1, voice out scores 1 and 5.

| # | Feature | Solves | Cool | Why that score |
|---|---|:---:|:---:|---|
| 7 | Streaming chat with citations | 5 | 5 | 156 sources you can only reach by remembering the right words. This is the feature the last month of retrieval work was for |
| 15 | Image → structured note | 5 | 5 | Apple Vision OCR has been on the Mac for weeks with no route from a photo to a note. Photograph a slide, get markdown |
| 4 | Library | 5 | 2 | Removes a tab, deletes code, makes the corpus feel like one thing. Dull and the right next move |
| 12–14 | Recorder → Whisper → note | 4 | 5 | Lectures. The single most student-shaped feature here |
| 19+20 | Auto-linking + related-on-open | 4 | 4 | The graph is a promise nothing has kept: `[[links]]` by hand is a habit nobody sustains |
| 17 | Reminder by voice | 3 | 5 | "Remind me to submit the form Friday." Small, and the best demo in the list |
| 9 | Voice in | 3 | 5 | Speaking to your own data is the moment it stops being a website |
| 22 | Smart collections | 4 | 3 | 18 subjects and 229 tags exist and currently only filter a search box |
| 24 | File browser | 4 | 2 | badal cannot hold a PDF you want next week. A personal cloud should |
| 16 | Reminder from a photo | 3 | 5 | Poster on a noticeboard becomes a reminder carrying the picture |
| 18 | Lecture mode | 4 | 4 | 90 minutes to sectioned notes with jumpable timestamps. Needs 12–14 first |
| 21 | Entities as graph nodes | 3 | 5 | "Everything about Prof X" across mail, events, tasks and notes |
| 29 | Eviction + cold restore | 4 | 2 | 50GB fills. Unblocked now that R2 is decided |
| 10 | Voice out + barge-in | 2 | 5 | Pure delight. Nothing is broken without it |
| 8 | Query rewriting + turn memory | 3 | 2 | Invisible, and what makes #7 feel like a conversation instead of a form |
| 25 | Photos grid + albums | 3 | 3 | Depends on 24 |
| 23 | Timeline | 2 | 4 | Lovely to scrub, solves nothing search does not |
| 30 | Web clipper | 3 | 3 | Cheap once notes exist |
| 27 | WebDAV | 3 | 2 | Finder mounts badal. Quietly the most "real cloud" item |
| 28 | Sharing links | 2 | 3 | Signed and expiring. Only matters when you want to send someone something |
| 5 | Calendar into Now | 3 | 1 | Housekeeping from the design guide |
| 32 | Import | 3 | 1 | One afternoon, then never again |
| 33 | Local LLM | 2 | 3 | Privacy and independence, at 8–14B quality |
| 26 | Organise files | 2 | 1 | Only bites once there are hundreds |
| 31 | Templates | 2 | 1 | |
| 11 | Fish Audio voice | 1 | 4 | Timbre. Strictly a nicety over #10 |

### The groups, ranked

A group is not the sum of its parts — you will never build all of one before
touching another. What matters per group is the ceiling, what it is blocked
on, and what it changes about what badal *is*.

| | Group | Ceiling | Effort | Blocked by | What it changes |
|---|---|---|---|---|---|
| 1 | **Talk to it** (7–11) | 5/5 | ~1 wk | 9–11 need R1 | The corpus stops being something you search and starts being something you ask. Cashes in every week of retrieval work |
| 2 | **Connect it up** (19–23) | 4/5 | ~1 wk | nothing | The corpus organises itself. Entirely server-side over embeddings that already exist |
| 3 | **Capture** (12–18) | 5/5 | ~2 wk | R1 + a Whisper model | The corpus grows from more than mail. Highest ceiling, heaviest gate |
| 4 | **Housekeeping** (4, 5, 30–33) | 5/2 | ~1 wk | nothing | Makes the app smaller and tidier. Never exciting, occasionally the right call |
| 5 | **Files** (24–29) | 4/3 | ~1.5 wk | 29 needs the archive paid for | Turns a knowledge app into a cloud. Real work, low delight |

**Capture ranks third but contains #15, which is 5/5 and barely gated** — the
OCR it needs has been running on the Mac for weeks. Pull that one out and do
it early rather than waiting for the group.

### Agreed build order

**1 → 3 → 5 → 2 → 4.** Talk to it, then Capture, then Files, then Connect,
then Housekeeping.

Consequences worth holding on to:

- Capture second makes **R1 a hard blocker rather than a distant one**. The
  useful half of that group needs the Mac awake between jobs.
- Files third means the **cloud archive must be paid for before #29**, or an
  evicted blob has exactly one copy.
- Connect fourth costs nothing permanent: auto-linking is a backfill, so
  everything captured in groups 3 and 5 gets linked retroactively when it
  lands.
- #15 still jumps its group. It is 5/5 and needs only OCR, which already runs.

### If you want one line

**Build 7.** Highest on both axes, everything it needs already exists, nothing
blocks it.

**Then 15**, because the OCR is sitting there unused and it is the other 5/5.

**4 whenever the UI annoys you enough** — it is the only item that makes the
app smaller.

### Known, unfixed

| | Thing | Why it is still here |
|---|---|---|
| — | ~~`free food on campus` misses the mess circular~~ | **Withdrawn.** The mess circular is about changing your mess plan, not free food. "The sources do not mention any free food on campus" is the correct answer, and I mislabelled it as a miss |
| — | Retrieval for a broad question is variable run to run | Expansion sometimes returns three extra phrasings and sometimes none, so a source can make the top 8 on one ask and not the next. Answers stay useful either way |
| — | Yearly repeats, "every N weeks", the `dropped` state | Schema supports all three; no UI. Nobody has asked |
