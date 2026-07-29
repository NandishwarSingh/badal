# badal — Personal Cloud Platform

**Status:** planning · **Last updated:** 2026-07-29

A self-hosted personal cloud: notes, files, email intelligence, and calendar, running
across an always-on VPS and an intermittent GPU laptop, with native Apple clients.

---

## 1. Decisions already made

| Decision | Choice | Why |
|---|---|---|
| Client stack | SwiftUI multiplatform (iOS + macOS) + web PWA | Two main devices are Apple; unlocks File Provider, Share Sheet, Shortcuts, Widgets |
| Build depth | Hybrid — custom apps, boring infra | Own the product surface, rent the plumbing |
| iOS timing | Deferred — macOS + web first | No Apple Developer account yet |
| University mail | Unknown — see §11 diagnostics | Biggest open feasibility risk |
| Server language | **Go** (recommended) | Single static binary, low RAM on a small VPS, strong I/O concurrency, same language for the laptop agent and CLI |
| Client language | **Swift** | Shared package across macOS and (later) iOS |

Two languages total: Go for everything server-side and system-side, Swift for clients.

### Confirmed hardware (verified 2026-07-29)

| | badal-core (VPS) | badal-forge (laptop) |
|---|---|---|
| Provider / model | Hostinger KVM 1, id `1367060` | personal laptop |
| CPU | **1 vCPU** | — |
| RAM | 4 GB | TBD |
| Disk | **50 GB** | TBD |
| GPU | — | **GTX 1650, 4 GB VRAM (TU117)** |
| OS | **CentOS Stream 9** | **Fedora Server** |
| Network | `200.141.2.137` / `2a02:4780:63:c00c::1`, 30 ms RTT | home LAN, intermittent |

Domain `nandishwarsingh.com` (Hostinger DNS). Timezone **Asia/Kolkata (IST)**.

**Three consequences, each of which changed the plan below:**

1. **1 vCPU / 50 GB is tight.** The service list in §3 was trimmed, and blob storage moved
   off the VPS entirely (§3.1).
2. **Both machines are RPM/SELinux/systemd.** CentOS Stream 9 and Fedora are the same
   family — so: **podman + quadlets, not Docker Compose** (§3.2). One mental model, one
   set of SELinux quirks, and systemd supervises containers directly, which composes with
   the watchdog design in §6.3.
3. **The GTX 1650 is not an LLM GPU** (§5.2). 4 GB VRAM and no tensor cores. It is a very
   capable OCR / embeddings / Whisper box, and that is what it should be used for.

---

## 2. Topology

```
                      ┌─────────────────────────────┐
   Internet ─── 443 ──▶│  badal-core  (VPS)          │
                      │  always on · source of truth │
                      │  Caddy · Postgres · Garage   │
                      │  badal-api · web PWA · ntfy  │
                      └──────┬───────────────┬───────┘
                             │ tailnet       │ tailnet
                ┌────────────▼──────┐  ┌─────▼──────────────┐
                │ badal-forge       │  │ badal-mac (MacBook)│
                │ GPU laptop        │  │ badal-web (browser)│
                │ INTERMITTENT      │  │ badal-ios (later)  │
                │ worker + replica  │  └────────────────────┘
                └───────────────────┘
```

### The one rule that makes offline-tolerance work

**The VPS must never call the laptop.** The laptop is a *worker that leases jobs* — it
pulls work, it is never a service on a request path. Offline tolerance then holds by
construction, not by error handling scattered through the codebase.

Corollary: the laptop's storage is a **replica**, never a primary. Nothing on the VPS
ever reads from it to serve a request.

### Network

- **Tailscale** mesh across VPS, laptop, MacBook, phone. Handles CGNAT/NAT traversal for
  the home laptop, gives MagicDNS names (`badal-forge`), ACLs, and Tailscale SSH so the
  laptop never exposes port 22.
- VPS is the only machine with public ports: **443** (Caddy) and 80 (redirect only).
- DNS: point `badal.<domain>` at the VPS **DNS-only** (no Cloudflare proxy) — the free
  proxy tier caps request bodies at 100 MB, which breaks large file uploads.
- Caddy handles Let's Encrypt automatically.
- Migration path: swap Tailscale for self-hosted **Headscale** later if you want zero
  third-party dependency. Same clients, same config, drop-in.

---

## 3. VPS services

### 3.1 Blob storage: self-hosted S3, two tiers

**Decision (user, 2026-07-29): no third-party object storage.** Run S3 on both machines.

| Tier | Where | Holds | Size |
|---|---|---|---|
| **Hot** | VPS Garage | **recent objects only** | capped ~20 GB of the 50 GB disk |
| **Archive** | Laptop Garage | **everything — recent *and* old** | full history |

Two **independent** single-node Garage instances, not one cluster. Garage replicates
uniformly across a cluster and cannot express "recent here, everything there", so the
tiering is driven by `badal-api`:

1. Write goes to **VPS hot** first. Object is immediately durable and servable.
2. A `replicate` job pushes it to **laptop archive**; on confirmed write (HEAD + checksum
   match) the row gets `archived_at`.
3. An eviction sweep removes objects from hot when
   `age > N days AND archived_at IS NOT NULL AND verify(laptop) == ok`.

**Invariant: never evict from hot unless the object is verified present on the archive.**
Eviction is the one operation in this system that can destroy data, so it verifies rather
than trusts a flag.

**Cold reads degrade, they don't fail.** Requesting an evicted object while the laptop is
offline returns `202 Accepted` with a queued retrieval, and ntfy fires when it lands. The
client shows "restoring from archive" rather than an error.

#### ⚠️ This makes the laptop a single point of failure for your old data

Once an object is evicted from the VPS, the laptop holds **the only copy**. That laptop is
a single consumer SSD with no redundancy, and it is also the machine deliberately designed
to reboot itself on crashes and run hot under sustained GPU load. It is the least reliable
component in the system, holding the only copy of the oldest data. That is one copy —
not 3-2-1.

Nothing above is blocked on fixing this, but pick one before the archive holds anything
irreplaceable:

- **A second disk on the laptop** (btrfs/ZFS mirror, or a periodic `rclone sync` to an
  external USB drive) — cheapest, keeps everything self-hosted, survives one disk death.
- **Offsite archive backup** — restic to B2 (~$6/TB/mo) for the archive tier only. Breaks
  the no-third-party rule, but it is the only option that survives fire, theft, or
  ransomware hitting the house.
- **Accept the risk explicitly** for classes of data you can regenerate, and keep anything
  irreplaceable pinned to hot so it never leaves the VPS. A `pinned` flag on the object
  metadata makes this a per-file decision.

Keep an `ObjectStore` interface in Go regardless, so hot/archive/offsite are swappable.

### 3.2 Podman + quadlets, not Docker Compose

Both machines are RPM/SELinux/systemd. Quadlets (`.container` files that systemd
generates units from) mean containers are ordinary systemd services: `Restart=always`,
`WatchdogSec`, dependency ordering, and journald logging all work natively — the same
mechanisms §6.3 uses for crash recovery. No daemon, rootless where possible.

### 3.3 Service list (trimmed for 1 vCPU / 4 GB)

| Service | Choice | Est. RAM | Notes |
|---|---|---|---|
| Reverse proxy / TLS | Caddy | ~30 MB | Automatic Let's Encrypt |
| Metadata store | Postgres 17 | ~512 MB | Tune `shared_buffers=512MB`, `max_connections=40` |
| Blob store | **Garage** (self-hosted S3, hot tier) | ~150 MB | See §3.1 |
| Queue + pubsub | **Postgres** (`SKIP LOCKED` + `LISTEN/NOTIFY`) | 0 | No NATS. For one user it will never be the bottleneck |
| API | `badal-api` (Go monolith) | ~80 MB | Single static binary |
| Web | SvelteKit PWA | 0 | Prebuilt static, served by Caddy — no Node at runtime |
| Push | self-hosted **ntfy** | ~20 MB | §9 |
| Metrics | **VictoriaMetrics single** + `vmalert` | ~100 MB | **Grafana dropped** — too heavy here. Query via Explore from your Mac, or run Grafana locally against the remote datasource |
| Backups | restic (systemd timer) | transient | §10 |

Leaves ~2 GB headroom. **Add 2 GB of swap** anyway — cheap insurance against an OOM kill
on a 1-vCPU box where one bad query can spike memory.

---

## 4. Sync engine

There are **three different sync problems** here and they need three different
mechanisms. Trying to solve all of them with one is the classic failure mode.

### 4.1 The backbone: `change_log`

Every mutation to any entity appends one row:

```sql
change_log(
  seq         bigserial primary key,   -- monotonic cursor
  entity_type text,                    -- 'note' | 'event' | 'file' | ...
  entity_id   uuid,
  op          text,                    -- 'upsert' | 'delete'
  actor_device uuid,
  hlc         text,                    -- hybrid logical clock
  created_at  timestamptz
)
```

Clients hold a `since_seq` cursor and call `GET /sync?since=N`. Realtime is a WebSocket
that only ever says *"new seq available, come pull"* — **the socket is a doorbell, not a
data channel.** This makes reconnection, missed messages, and cold start all the same
code path, and it is dramatically easier to get right than streaming deltas.

### 4.2 Notes → CRDT (Automerge)

- **Automerge**, not Yjs. Automerge has a Rust core with real Swift *and* Go bindings;
  Yjs would drag you into JavaScript on the client, which conflicts with the SwiftUI
  decision.
- Server stores the Automerge document binary plus an append-only op log. It acts as a
  relay and persistence layer and does not need to interpret content.
- Server also materialises plain text into Postgres for full-text search (`tsvector`),
  and later embeddings.

### 4.3 Records → server-authoritative + client outbox

For calendar events, email metadata, tasks, settings.

- Reads: pull via the change feed.
- Writes: optimistic concurrency (`If-Match: <version>`). Offline writes queue in a local
  **outbox** and replay on reconnect.
- Conflicts (rare for one user) resolve last-writer-wins by HLC.

### 4.4 Files → content-addressed chunking

- Chunk with **FastCDC** (content-defined, ~1–4 MB average), hash each chunk with **BLAKE3**.
- A file = ordered list of chunk hashes + metadata. Deduplication and delta-sync both fall
  out for free.
- Upload flow: `POST /chunks/probe` with the hash list → server returns which it's missing
  → client uploads only those → client commits the manifest atomically.
- Chunks in Garage, manifests in Postgres.
- This is how restic/borg/Dropbox work. It is not much code and it is the right call.

---

## 5. GPU laptop (`badal-forge`)

### 5.1 Job lease protocol

```sql
jobs(
  id uuid, kind text, payload jsonb, state text,
  priority int, capabilities text[],
  lease_owner uuid, lease_expires_at timestamptz,
  attempts int, max_attempts int,
  result jsonb, error text, created_at timestamptz
)
```

1. Agent long-polls `POST /jobs/claim {capabilities:["gpu","ocr","whisper","embed"]}`.
2. Claim is `UPDATE ... FOR UPDATE SKIP LOCKED` setting `lease_expires_at = now() + 60s`.
3. Agent **heartbeats every 20 s** to extend the lease.
4. A reaper on the VPS re-queues any job whose lease expired.

Pull the laptop's power mid-job → the job is back in the queue within 60 seconds.
No orphaned work, no manual intervention, no special-case code.

### 5.2 Every job kind declares a fallback

| Job kind | Primary | Fallback when forge is offline |
|---|---|---|
| `ocr` / `vlm` | forge (DeepSeek-OCR or a small VLM) | OpenRouter vision model |
| `embed` | forge | **defer** — search index lags, nothing breaks |
| `transcribe` | forge (Whisper) | defer, or Groq Whisper (cheap) |
| `llm-extract` | **always OpenRouter** | n/a |
| `thumbnail` / `transcode` | forge | CPU on VPS (slow but fine) |

### 5.2b What a GTX 1650 can actually do

The 1650 is TU117: **4 GB VRAM, ~896 CUDA cores, and no tensor cores.** The missing tensor
cores matter — FP16/INT8 quantisation buys far less speedup here than on an RTX card. 4 GB
is the binding constraint on everything.

This settles the local-LLM question conclusively. DeepSeek V4 Flash is 284 B parameters
(13 B active); nothing in that class is running on this card. And at **$0.09 / $0.18 per
1M tokens**, ~100 emails/day costs **well under $1/month**. There is no case for local
text inference — route `llm-extract` to OpenRouter always.

What the card *is* genuinely good for:

| Workload | Model | VRAM | Verdict |
|---|---|---|---|
| **Printed-text OCR** | RapidOCR / PaddleOCR | <1 GB | **Primary OCR path.** Fast, accurate on printed text, which is most university mail |
| **Layout / scene VLM** | Florence-2-base (0.23 B) or large (0.77 B) | 1–2 GB | Captioning, grounding, messy scans |
| **Harder VLM** | Moondream2 (~1.8 B) | ~3.5 GB fp16 | Fits, but tight. Use only when OCR is insufficient |
| **Embeddings** | bge-small / bge-base-en-v1.5 | <1 GB | Excellent fit — fast, batches well. Real cost and latency win over cloud |
| **Image tagging / search** | CLIP ViT-B/32 | <1 GB | Trivial |
| **Transcription** | faster-whisper `small` int8 | ~1 GB | Comfortable. `medium` int8 (~1.5 GB) also fits |
| **Thumbnails / transcode** | NVENC + ffmpeg | — | Note: original TU117 1650 has the older Volta-gen NVENC (no B-frames). GDDR6 revisions have Turing NVENC |

**DeepSeek-OCR is dropped** from the original plan — ~3 B params won't fit comfortably in
4 GB, and without tensor cores an INT4 build wouldn't be fast enough to be worth it.
RapidOCR → Florence-2 → cloud VLM is the right escalation ladder.

So the laptop's role is: **OCR, embeddings at bulk, Whisper, image processing, and batch
backfills.** All of which are exactly the workloads where per-token cloud cost and
round-trip latency actually add up.

### 5.3 Storage replica

Run a daemon that pulls a full copy of the object store (Garage replica, or `rclone sync`
on a schedule). Gives you a free second copy in a different failure domain, plus fast
local reads for compute jobs. If it's offline it just catches up later.

---

## 6. Laptop system software

**Fedora Server**, headless. All of this belongs in an Ansible playbook under
`deploy/ansible/forge.yml` so the box is reproducible.

### 6.0 ⚠️ Three Fedora-specific traps

**1. Secure Boot + `akmod-nvidia` will silently kill your GPU.**
The NVIDIA driver comes from RPMFusion as an akmod that rebuilds on every kernel update.
Under Secure Boot, an unsigned freshly-built module **refuses to load** — so after a
routine kernel update the box boots fine, `nvidia-smi` fails, and every GPU job starts
failing for no visible reason. On a headless machine you will not notice for days.

Fix, once: generate and enroll a MOK key (`kmodgenca` + `mokutil --import`, which needs
one interactive reboot at the console — do this while the laptop is still on your desk),
and configure akmods to sign automatically. Then add a **post-boot verification job**:
run `nvidia-smi`, and if it fails, report the node as GPU-incapable and fire an ntfy alert
rather than accepting GPU jobs it cannot run.

**2. Fedora's ~13-month lifecycle means a major upgrade every year** on a box you want to
never touch. Mitigate: `dnf-automatic` in `security` mode with `exclude=kernel*` so
unattended patching never swaps the kernel out from under the signed NVIDIA module.
Schedule kernel updates deliberately, while you have physical access.
*(If this box ever becomes truly set-and-forget, Rocky/Alma 10 would be the better OS —
but Fedora's newer kernels and Mesa are genuinely nicer for GPU work, so it's a reasonable
trade to make knowingly.)*

**3. SELinux is enforcing.** It will block the agent from writing outside expected
contexts and break container bind mounts. Do **not** set it permissive — instead label
deliberately: `semanage fcontext -a -t container_file_t "/var/lib/badal(/.*)?"` and use
`:Z` on podman volume mounts. Budget an afternoon for this; the same knowledge applies to
the CentOS Stream VPS.

### 6.1 Auto power-on when AC is connected

This is **firmware, not OS**. Check BIOS/UEFI for *"Restore on AC Power Loss"* / *"AC
Recovery"* / *"Wake on AC"* → set to **Power On**. Common on ThinkPads and desktops,
often absent on consumer laptops.

If the BIOS doesn't expose it, in order of preference:

1. **RTC wake alarm** (`rtcwake`) — schedule daily wakes at fixed times. Combined with
   "never suspend on AC," covers most of the need. Works from S3 sleep, not from full off.
2. **Wake-on-LAN** — `ethtool -s eth0 wol g` + enable in BIOS. Caveat: a magic packet must
   originate on the same L2 segment, so **the VPS cannot send it directly**. Needs a small
   always-on device on the home LAN (Raspberry Pi Zero W, ~$15) as a relay, or router
   support.
3. **Smart plug** — cut and restore power, combined with BIOS "Power On" if available.

### 6.2 Never sleep on AC

`/etc/systemd/logind.conf`:
```ini
HandleLidSwitch=suspend                 # still sleeps on battery
HandleLidSwitchExternalPower=ignore     # stays awake on AC, lid closed
HandleLidSwitchDocked=ignore
IdleAction=ignore
```
If you want it to *never* sleep: `systemctl mask sleep.target suspend.target hibernate.target hybrid-sleep.target`.

### 6.3 Restart on crash — three layers

| Layer | Mechanism |
|---|---|
| Service dies | systemd `Restart=always`, `RestartSec=5`, `StartLimitIntervalSec=0` |
| Service hangs | systemd `WatchdogSec=30` + agent calls `sd_notify(WATCHDOG=1)` |
| Kernel hangs | hardware watchdog: `/etc/systemd/system.conf` → `RuntimeWatchdogSec=30`, `RebootWatchdogSec=10m` |
| Kernel panic | `kernel.panic=10` in sysctl → auto-reboot after 10 s |

Add boot-loop protection: if the agent fails to reach the VPS on 3 consecutive boots,
stop retrying and fire an alert rather than thrashing.

### 6.4 Auto-connect WiFi + connectivity watchdog

- NetworkManager: `connection.autoconnect yes`, `connection.autoconnect-retries 0` (infinite).
- **Prefer Ethernet** as primary with WiFi as fallback via connection priority — one less
  failure mode.
- Escalating watchdog on a 60 s systemd timer:
  ```
  ping tailnet/VPS
    3 failures  → nmcli con down/up
    6 failures  → systemctl restart NetworkManager
   10 failures  → systemctl restart tailscaled
   20 failures  → reboot
  ```

### 6.5 ⚠️ Disk encryption vs unattended boot

If the disk is LUKS-encrypted, **every unattended reboot hangs at the passphrase prompt** —
which silently defeats the entire auto-restart design. You will discover this during your
first remote crash, at the worst possible time.

Pick one, deliberately:
- (a) No FDE on this box; encrypt only a data partition that the agent unlocks after boot.
- (b) **TPM-bound unlock**: `systemd-cryptenroll --tpm2-device=auto` — recommended.
- (c) `dropbear-initramfs` for remote SSH unlock (still manual).

### 6.6 Thermals

A laptop pinned at 100 % GPU with the lid closed will cook itself.
- Cap power: `nvidia-smi -pl <watts>`.
- The agent must poll temperature and **refuse GPU jobs above a threshold** (e.g. 85 °C),
  reporting itself as capacity-unavailable rather than failing jobs.
- Physically: prop the lid or use a vertical stand.

### 6.7 Don't lock yourself out

If Tailscale itself dies you have no access. Timer: tailnet unreachable 10 min → restart
`tailscaled`; 20 min → reboot. (Covered by §6.4's escalation.)

---

## 7. Email → AI → Calendar

### 7.1 Pipeline

```
IMAP IDLE (or Graph delta poll)
  → raw MIME archived to Garage, immutable, forever
  → MIME parse: headers, text/plain, text/html, attachments
  → attachments:  PDF → pdftotext (fast path)
                  images/scans → OCR|VLM job → forge, else OpenRouter vision
  → normalised text bundle
  → DeepSeek V4 Flash via OpenRouter, structured JSON output
  → validate → dedupe → write events as `proposed`
  → notify (ntfy) → user confirms in review inbox
```

### 7.2 Five things that decide whether this feature survives

**1. Never auto-write low-confidence events.**
Extracted events land in `state = 'proposed'` and surface in a review inbox
("3 dates found this week — confirm?"). One-tap confirm. Once you've confirmed a few
weeks' worth, auto-confirm high-confidence extractions from known senders.
Without this, your calendar fills with garbage within a month, you stop trusting it, and
the whole module is dead weight.

**2. Archive raw MIME forever.**
Storage is cheap. This lets you re-run extraction with a better model or a better prompt
six months from now without re-fetching anything. It is the single highest-value thing to
get right on day one.

**3. Require an `evidence` field.**
The model must return a verbatim quoted span from the source for every extracted date.
You then assert that span actually appears in the email and reject the extraction if it
doesn't. Cheap, mechanical, and kills most hallucinated dates.

```json
{
  "events": [{
    "title": "...", "starts_at": "2026-08-14T15:00:00+05:30", "ends_at": "...",
    "all_day": false, "location": "...",
    "kind": "exam|deadline|class|event|meeting",
    "confidence": 0.0,
    "evidence": "verbatim span from the email"
  }],
  "actions": [{"what": "...", "due_at": "...", "confidence": 0.0}],
  "summary": "...", "importance": 0.0
}
```

**4. Timezones and relative dates.**
University mail says *"Friday 3pm"* with no date and no timezone. Pass the email's
`received_at`, your timezone, and the institution's timezone into the prompt, and require
absolute ISO-8601 output. **Resolve relative dates against `received_at`, never `now()`** —
otherwise a re-run months later silently produces different answers.

**5. Idempotency.**
Key extractions on `(message_id, extraction_version)`. Re-running after a prompt change
must produce a new *version* you can diff, not duplicate events.

### 7.3 Boundaries

- **Read-only IMAP initially.** No SMTP send until the read path is boring and trusted.
- Log every model call: prompt version, tokens, cost, latency, result. Lets you A/B prompt
  changes and answer "why did this event appear?"

---

## 8. Calendar

Own the data (Postgres `events`), but expose it where Apple's native apps can reach it.

**Start with a signed `.ics` subscription URL.** Roughly 100 lines of Go. Apple Calendar
on macOS *and* iPhone subscribes to it and polls. **This gives you the calendar feature on
your phone before the iOS app exists** — a large early win given iOS is deferred.

Upgrade to full two-way **CalDAV** later (hand-rolled, or Radicale behind the API) when
you want to create and edit events from the native client.

---

## 9. Push notifications without an Apple Developer account

Self-host **ntfy** on the VPS. Install the free ntfy app from the App Store on your
iPhone. Subscribe to a secret topic. You now have real push notifications — today, for
$0, with no developer account.

Use it for: proposed-event review prompts, forge offline alerts, backup failures, the
daily digest, job failures. When you do build the iOS app later, APNs replaces it for
in-app notifications and ntfy stays as the ops/alerting channel.

---

## 10. Cross-cutting

### Auth
- **Passkeys (WebAuthn)** for web — Touch ID on the Mac, nothing to leak.
- Devices hold a long-lived opaque **device token** in Keychain, exchanged for short-lived
  JWTs.
- Every device individually registered and revocable. You will want this the first time a
  device goes missing.
- **The forge agent gets a restricted scope** (`jobs:claim`, `blobs:write`) — it must not
  be able to read your email.

### Backups — Phase 0, not "later"
The VPS is the source of truth. If it dies without backups, badal is over.
- **restic** → Backblaze B2 or a Hetzner Storage Box. Nightly, encrypted, retention
  7 daily / 4 weekly / 12 monthly.
- Postgres: `pg_dump` nightly + WAL archiving to object storage (or pgBackRest for PITR).
- The forge replica is copy #2, different failure domain.
- **A backup you have never restored is not a backup.** Automate a monthly restore drill:
  restore the latest snapshot into a throwaway container and assert row counts. Alert if
  it fails.

### Alerts
forge offline > 6 h · queue depth · failed jobs · backup age > 36 h · cert expiry ·
disk > 80 % · email ingest stalled > 2 h.

---

## 11. University mail — RESOLVED (2026-07-29)

**Account:** `nandishwar.singh2026@vitstudent.ac.in` (VIT).
**Verdict: Google Workspace. This is the best-case outcome** — the M365 Conditional
Access nightmare does not apply.

```
MX     1 aspmx.l.google.com          ← Google Workspace, definitively
       5 alt1/alt2.aspmx.l.google.com
SPF    v=spf1 include:_spf.google.com include:spf.protection.outlook.com -all
TXT    MS=ms59884092
DMARC  v=DMARC1; p=reject; adkim=s; aspf=s
autodiscover.vitstudent.ac.in        → none
enterpriseregistration.…             → none
```

Note the SPF includes **both** Google and Outlook, and there is an `MS=` tenant marker —
so VIT has a Microsoft tenant too. But the absence of `autodiscover` / `enterprise-
registration` CNAMEs plus Google-only MX means **mailboxes live in Google**. Microsoft is
almost certainly just licensing/SSO or a sending path for some campus system. Ingest from
Google.

### Two integration paths

| | Gmail API + OAuth | IMAP + App Password |
|---|---|---|
| Survives tenant hardening | ✅ usually | ❌ often disabled first |
| Incremental sync | ✅ `history.list` from a stored `historyId` | ⚠️ UIDVALIDITY/UIDNEXT bookkeeping |
| Real push | ✅ `users.watch` → Pub/Sub | IMAP IDLE (a held connection) |
| Setup cost | Google Cloud project + OAuth consent | ~zero, if permitted |

**Recommended: Gmail API, `gmail.readonly` scope, desktop OAuth flow.** Store the refresh
token in `.env`. Poll `history.list` against a persisted `historyId` for cheap incremental
sync; add `users.watch` + Pub/Sub later if polling latency ever matters. Read-only scope
enforces §7.3's "no sending" boundary at the API level rather than by convention.

### The one remaining unknown — a 30-second check

Both paths depend on VIT's Workspace admin policy, which DNS cannot reveal:

1. Open **https://myaccount.google.com/apppasswords** signed in as the VIT account.
   - Loads → IMAP + app password works. Fastest possible v1.
   - Blocked → app passwords are off; use the Gmail API.
2. Gmail → Settings → **Forwarding and POP/IMAP** → is "Enable IMAP" present?
3. Admins can also block unverified third-party OAuth apps. If the OAuth consent screen
   errors with *"access blocked: this app is blocked"*, the tenant restricts app access.

**If both are locked down:** set a Gmail filter to auto-forward to a mailbox you control
and ingest from there — but VIT's `p=reject` DMARC with strict alignment means forwarded
mail may fail SPF/DKIM at the destination, so the receiving mailbox must be lenient about
forwarded mail. Test before relying on it.

---

## 12. Repository layout

```
badal/
  cmd/
    badal-api/            # VPS monolith
    badal-forge/          # laptop agent
    badal/                # CLI
  internal/
    auth/  store/  sync/  chunks/  jobs/
    mail/  calendar/  notes/  ai/
  db/migrations/          # goose
  web/                    # SvelteKit PWA
  apple/                  # Swift package (shared) + Xcode project
  deploy/
    compose.yml
    caddy/
    systemd/              # forge units, watchdogs, net monitor
    ansible/              # core.yml, forge.yml
  docs/
```

---

## 13. Roadmap

| Phase | Weeks | Scope | Done when |
|---|---|---|---|
| **0 · Foundation** | 1–2 | Ansible VPS provisioning, Tailscale mesh, Caddy+TLS, Postgres, Garage, restic + first restore drill, ntfy, monitoring. **Run the §11 mail diagnostics.** | `https://badal.<domain>/health` → 200, a backup has been restored and verified |
| **1 · Spine** | 3–5 | Go API skeleton, auth + device registration, `change_log`, sync cursor, chunked blob store, `badal` CLI | You can `badal push`/`badal pull` a file from the Mac terminal |
| **2 · Notes** | 6–9 | Automerge, notes API, SwiftUI macOS app (editor, offline, sync, global quick-capture hotkey), web notes UI | **You use it daily.** This milestone decides whether the project survives — reach real daily use fast |
| **3 · Forge** | 10–12 | Job queue + lease/heartbeat/reaper, forge agent, systemd hardening (§6), Ansible forge playbook, storage replica | Yank the laptop's power mid-job → job completes anyway via re-queue or fallback |
| **4 · Email → Calendar** | 13–17 | IMAP/Graph ingest, MIME parse, raw archive, attachment OCR routing, DeepSeek extraction + evidence validation, dedupe, proposed-events review inbox, `.ics` URL | Apple Calendar shows university deadlines you confirmed with one tap |
| **5 · Files & photos** | 18+ | macOS File Provider extension (on-demand files in Finder), photo ingest, thumbnails + CLIP tagging on forge | Finder shows badal as a real drive |
| **6 · iOS** | — | Apple Developer account, SwiftUI iOS app reusing the Phase 2 package, APNs, Share Sheet, Shortcuts, background refresh | |
| **7 · Semantic search** | — | Embeddings on forge over notes + mail + files, pgvector, hybrid BM25 + vector, RAG answer endpoint | "Ask my stuff" works |

---

## 14. Feature suggestions, ranked by value-to-effort

1. **Self-hosted ntfy push** — real iPhone notifications, no dev account. Phase 0.
2. **`.ics` subscription URL** — calendar everywhere without an iOS app. ~100 lines.
3. **Quick capture from everywhere** — global hotkey on Mac, `badal note "..."` in the
   terminal, an email-to-self address, later a Shortcuts action. Capture friction is what
   kills note apps.
4. **The review-inbox pattern** — generalise it. *No AI output writes to your source of
   truth unreviewed*, for any feature.
5. **Web clipper** — browser extension → page as markdown into notes. Trivial once the
   notes API exists.
6. **Voice memo → Whisper → note** — excellent use of the GPU, and iOS Shortcuts can
   record and upload with no app at all.
7. **Semantic search across everything** — the actual payoff of owning all your data in
   one place.
8. **Daily digest** — one morning ntfy: today's events, this week's deadlines, mail needing
   action. This is what makes it feel like *your* cloud rather than a pile of services.
9. **`badal` CLI early** — build it in Phase 1 and every endpoint gets exercised before any
   UI exists. Free integration test harness.
10. **Immutable AI action log** — every model call with prompt version, cost, latency,
    output. Enables A/B testing prompts and auditing surprises.

### Deliberately out of scope
Media server (run Jellyfin separately), password manager (use Bitwarden), home automation,
multi-user support. Each is its own project and none makes the core better.

---

## 14b. Phase 0 progress — as built (2026-07-29)

**Live:** `https://badal.nandishwarsingh.com/health` → `200 ok`, Let's Encrypt cert valid
to 2026-10-27, HTTP→HTTPS 308. **Verified across a reboot.**

| Done | Detail |
|---|---|
| Root access recovered | Supplied password was wrong. Hostinger's `public-keys/attach` only bakes keys in at *reinstall*, so that route was a dead end — recovered via `PUT /virtual-machines/1367060/root-password` |
| SSH hardened | ed25519 key `~/.ssh/badal_ed25519`; `PermitRootLogin prohibit-password`, `PasswordAuthentication no`, `MaxAuthTries 3`. Alias `ssh badal-core` |
| **rpcbind removed from public IP** | Port 111 was listening on `0.0.0.0` — a known amplification vector, unused here. Disabled + masked |
| firewalld | Was **not installed** on this minimal image. Installed, enabled; `public` zone = ssh/http/https + 443/udp for HTTP/3. `cockpit` and `dhcpv6-client` removed |
| Swap | 2 GB swapfile, `vm.swappiness=10`. `mkswap` warned about XFS extents — verified benign, re-activates cleanly and survives reboot |
| Caddy | podman **quadlet** at `/etc/containers/systemd/caddy.container`, auto-starts at boot |
| Host | hostname `badal-core`, **timezone UTC** (deliberate — servers log in UTC; IST is an app-layer concern), chronyd on |
| DNS | `badal` A + AAAA added via **`overwrite: false`** (merge). Google Search Console TXT and `www` CNAME preserved. Zone backed up to `deploy/dns/zone-backup-2026-07-29.json` |
| Packages | podman, git, jq, tar, policycoreutils-python-utils, chrony |

### SELinux: now `Enforcing` — and how the first attempt broke the box

The image shipped `SELINUX=disabled`. It is now **Enforcing**, with 0 AVC denials and 0
failed units across a reboot.

**The first attempt bricked the VPS for ~25 minutes.** Recording it because the same trap
applies to the Fedora laptop:

> Setting `SELINUX=enforcing` + `touch /.autorelabel` and rebooting **does not work** when
> SELinux was previously `disabled`. With the filesystem carrying no security labels at
> all, the kernel loads the policy at boot and denies essentially everything — *including
> the processes that would run the relabel*. It deadlocks before networking or journald
> start, so there is no log and no console output. `/.autorelabel` was still present
> afterwards, proving the relabel never executed.

**Correct sequence — always go via permissive:**

```
1. SELINUX=permissive  + touch /.autorelabel  → reboot   # relabel runs, nothing blocked
2. verify: getenforce == Permissive, /.autorelabel consumed, ls -Z shows real labels,
           ausearch -m AVC -ts boot  == 0 denials
3. SELINUX=enforcing → reboot
4. verify again
```

**Recovery, if it happens anyway:** Hostinger's API has no console endpoint, but
`POST /virtual-machines/{id}/recovery` boots an Ubuntu 24.04 rescue OS with the real disk
attached as a *second* device (`/dev/sdb1`, auto-mounted at `/mnt/sdb1`). Edit
`/mnt/sdb1/etc/selinux/config` there, then `DELETE .../recovery` to boot back. Total
round trip ~4 minutes. A plain `POST /restart` does **not** help — the hang is
deterministic.

Podman bind mounts need `:Z` on every `Volume=` line in the quadlets, or containers are
denied under enforcing. Already applied to `caddy.container`.

**Also discovered:** Hostinger takes **automatic weekly backups** (most recent
2026-07-24), and the VM was migrated between hosts on 2026-07-22. The weekly backup is a
useful floor, but it does not replace §10 — it is provider-local and not restore-tested.

### Garage S3 hot tier — live

Single-node Garage `v1.0.1` as a podman quadlet, zone `vps`, **capacity capped at 20 GB**
of the 50 GB disk per §3.1. Bucket `badal-hot`, key `badal-api` (read/write/owner).
Verified with a real S3 `PUT` + `LIST` round-trip.

**Bound to loopback only** (`127.0.0.1:3900` API, `:3903` admin) — not reachable from the
internet even though the firewall would allow nothing anyway. When the laptop joins over
Tailscale, the bind moves to the tailnet interface, *not* to `0.0.0.0`.

Credentials are in `.env` (`S3_HOT_*`) and on the box at `/root/.garage-badal-api.creds`.

### Hostinger backups: read-only via API

`GET .../backups` lists them; **`POST` is not supported** (405 — `GET, HEAD` only). Weekly
automatic snapshots continue on their own, and a manual one can only be triggered from
hPanel. They remain a useful floor but are provider-local, weekly, and not restore-tested —
they do not replace §10.

### Remaining in Phase 0
Postgres · restic backups + first restore drill · ntfy · VictoriaMetrics · Tailscale ·
laptop Garage archive tier

---

## 15. Status of prerequisites

| Item | Status |
|---|---|
| VPS specs / OS | ✅ Hostinger KVM 1 · 1 vCPU · 4 GB · 50 GB · CentOS Stream 9 |
| Domain | ✅ `nandishwarsingh.com`, Hostinger DNS (currently only a `www` CNAME and a Google TXT) |
| Hostinger API key | ✅ verified working |
| OpenRouter key | ✅ verified working, paid tier, zero usage to date |
| Timezone | ✅ Asia/Kolkata |
| GPU | ✅ GTX 1650 4 GB — see §5.2b |
| **VPS root access** | ❌ **BLOCKED** — password auth is enabled but the supplied password is rejected |
| Laptop SSH | ⏳ pending Fedora install |
| Laptop RAM / disk | ⏳ needed to size the blob replica |
| Mail diagnostics (§11) | ⏳ **run this in week 1** — highest-risk unknown |
| Backblaze B2 account | ⏳ needed for §3.1 |

### Credential hygiene

Phase 0 installs an ed25519 key (`~/.ssh/badal_ed25519`, pubkey `badal-admin`) and disables
password auth entirely, which retires the root password as a credential.

The Hostinger and OpenRouter keys were shared in plaintext and should be **rotated once
Phase 0 is stable** — both are one-click regenerations in their respective dashboards, and
`.env` is the only place they need updating.
