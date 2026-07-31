# badal — interface design guide

Revision 1. Written when the tab bar was full at five and fifteen more features
were on the plan.

## 1. The problem this solves

Shipped: notes, mail, calendar, events, review, search, subjects, settings.
Planned: tasks, reminders, chat, voice in, voice out, recorder, image→note,
lecture mode, auto-linking, entities, smart collections, file browser, photos,
albums, WebDAV, sharing links.

That is roughly twenty-three destinations competing for five tab slots. No
arrangement of tabs survives it. Adding a sixth tab buys one feature and costs
every tab about 17px of width on a 360px phone. The answer has to be structural.

## 2. Why badal is not Instagram, and why that helps

Instagram fits hundreds of features into five tabs using four tricks: hidden
horizontal swipes (camera left, DMs right), the profile screen as an overflow
drawer, long-press for secondary actions, and bottom sheets for everything
transient.

It needs those tricks because its features are **heterogeneous surfaces**.
Reels, Shop, DMs and Stories are separate products sharing a shell. There is no
single underlying object, so there is nothing to unify.

badal is the opposite, and this is the whole insight:

> **Every feature in badal is a view over one corpus, or an action on one item.**

A note, an email, an event, a task, a photo, a transcript, a file — each is an
item with text, a time, and subjects. They already share one embeddings table,
one subject taxonomy, one search endpoint, one sync spine.

Organising the UI by *type* — Notes here, Mail there, Files elsewhere — throws
that away and reimposes the app's schema on the person using it. Nobody opens
this app thinking "I need a note". They think **"what do I have to do today"**
or **"where is that thing about the internship"**.

So: **navigate by intent, not by type. Type is a filter, not a destination.**

## 3. Architecture: three surfaces

```
┌──────────────────────────────────────────────────────────┐
│  NOW            LIBRARY              ASK                 │
│  what needs me  everything I own     talk to it          │
│  today          (search + browse)    (chat, voice)       │
└──────────────────────────────────────────────────────────┘
        ⊕ Capture (FAB — a verb, not a place)
        ⚙ Settings (top bar — rare, not a tab)
```

**Now** answers *why did I open this?* Overdue and today's tasks, today's
events, reminders, things awaiting review, recent captures. Time-ordered,
acted on in place — tick a task, confirm an event, snooze a reminder, without
navigating anywhere. Segments: `Today · Week · Month`, where Month is the
calendar grid.

**Library** is the corpus, and search *is* how you enter it — not a separate
tab. A query field, type chips, subject chips, one result list. Absorbs today's
Notes tab, Search tab, and the Mail segment; later absorbs files, photos,
albums, and transcripts. A smart collection is just a saved filter.

**Ask** is conversation over the corpus with citations, and later voice. It
arrives with Phase 5B.

Three tabs, and they stop growing. Every planned feature is a chip in Library,
a card in Now, an item in the Capture sheet, or a row in Settings.

### Phone

```
┌────────────────────────┐   ┌────────────────────────┐
│ Now               ⚙   │   │ Library           ⚙   │
│ ( Today  Week  Month ) │   │ ┌────────────────────┐ │
│                        │   │ │ Search everything  │ │
│ ▸ 2 overdue            │   │ └────────────────────┘ │
│ ┌────────────────────┐ │   │ All Notes Mail Tasks▸ │
│ │ ☐ Open project     │ │   │ Academics Internships▸│
│ │   due today        │ │   │ ┌────────────────────┐ │
│ └────────────────────┘ │   │ │ mail · SolderLab   │ │
│ ┌────────────────────┐ │   │ │ Embedded systems…  │ │
│ │ 14:00 Guest lecture│ │   │ └────────────────────┘ │
│ └────────────────────┘ │   │ ┌────────────────────┐ │
│ ┌────────────────────┐ │   │ │ note · Formatting  │ │
│ │ 3 events to review │ │   │ └────────────────────┘ │
│ └────────────────────┘ │   │                        │
│                    ⊕  │   │                    ⊕  │
│ ┌────┬────────┬─────┐ │   │ ┌────┬────────┬─────┐ │
│ │Now │Library │ Ask │ │   │ │Now │Library │ Ask │ │
│ └────┴────────┴─────┘ │   │ └────┴────────┴─────┘ │
└────────────────────────┘   └────────────────────────┘
```

Two chip rows in Library is the maximum nesting allowed anywhere: type, then
subject. Both scroll horizontally on a phone, wrap above 900px — the rule
already used for subject chips.

### Desktop

Same three surfaces; only the chrome changes. The tab bar is already a centred
pill row above 820px, sheets already centre at 720px, and `.cards` is already a
grid above 900px. Nothing here needs a separate desktop information
architecture — which is the point of keeping the surfaces few.

```
┌──────────────────────────────────────────────────────────┐
│              [ Now ][ Library ][ Ask ]              ⚙   │
├───────────────────────────┬──────────────────────────────┤
│ list column               │ detail column                │
│ (cards grid, 320px min)   │ (sheet content, inline)      │
└───────────────────────────┴──────────────────────────────┘
```

Above 1240px a sheet becomes a right-hand detail pane instead of an overlay —
there is room to see the list and the thing at once.

## 4. The placement ladder

**The rule: put a feature on the lowest rung that works.** Every rung up costs
a tap and a piece of someone's memory.

| | Rung | Use when | Example |
|---|---|---|---|
| 1 | **Inline on the item** | It acts on one thing and needs no context | Tick a task, confirm an event |
| 2 | **Bottom sheet** | Detail or a focused sub-task; you must not lose your place | Open a mail, event detail |
| 3 | **Chip** | It is a new *kind* of item, or a filter over existing ones | Photos, files, a smart collection |
| 4 | **Segment** | A sibling *view* of the same data | Today / Week / Month |
| 5 | **Top-bar action** | Rare, applies to the whole surface | Sync, settings |
| 6 | **Route under a surface** | A real destination with its own URL | `/note/[slug]` |
| 7 | **Tab** | Daily use, not a view of existing data, not an action | — |

**Rung 7 requires displacing an existing tab.** Cap: four. If a feature seems to
need a tab, it is usually a chip in Library or a card in Now that has not been
recognised as one yet.

Two corollaries:

- **A noun already in the corpus never earns a tab.** Photos are items with a
  time and subjects, so they are a chip. Files likewise.
- **A verb never earns a tab.** Capture, record, scan and share are actions;
  they belong on the FAB or on the item they act on.

## 5. Where everything lands

Applying the ladder to every shipped and planned feature:

| Feature | Placement | Rung |
|---|---|---|
| Tasks | Now — primary content | 1 |
| Reminders | Now cards + push notification actions | 1 |
| Review proposed events | Now card, expands to a sheet | 1–2 |
| Conflicts | Now card + inline on the note | 1 |
| Calendar grid | Now → Month segment | 4 |
| Notes | Library → Notes chip | 3 |
| Mail | Library → Mail chip | 3 |
| Search | Library — it *is* the surface | — |
| Subjects | Library → second chip row | 3 |
| Smart collections | Library → saved filter chip | 3 |
| Files, Photos, Albums | Library → chips | 3 |
| Transcripts | Library → chip | 3 |
| Chat, voice in/out | Ask | 7 |
| Capture: note, task, photo, voice, scan | FAB sheet | 2 |
| Lecture mode | Capture sheet → Voice → long-form | 2 |
| Auto-linking, entities | Inline on the item; related-items strip | 1 |
| Sharing links | Item overflow → sheet | 2 |
| WebDAV, eviction, storage, devices | Settings | 5 |

Twenty-three features, three tabs, no drawer.

## 5a. The grouped-list shell (revision 2)

The first build was a stack of free-floating cards with gaps between them.
Every row carried its own border, its own shadow and its own eight pixels of
air, so six items looked like six unrelated objects and the text had nowhere
to go.

Grouping fixes it, and it is what every dense mobile app converges on:

| Primitive | Use |
|---|---|
| `.pagehead` | Large bold title **in the content**, not a chrome bar. Scrolls away. Round actions on the right |
| `.iconbtn` | 40px circular header action — bell, settings, add, back |
| `.group` + `.grow` | One container, hairline dividers **inset past the icon** so they line up with the text |
| `.gtile` | 34px colour-coded tile. Identity at a glance is most of what makes a long list scannable, and colour does it faster than a label |
| `.feed` + `.feedrow` | Flat inset-divider list for things read in sequence — mail, search results, activity. No tile, no card |
| `.callout` | Circular badge, big headline, muted body, one right-aligned action |
| `.sectionhead` | Bold label above a group, with an optional count |

Where each goes: **grouped rows** for lists you pick from (settings, notes,
tasks, links), **feed rows** for lists you read down (mail, search, activity).
Getting that the wrong way round is what made search results look like
navigation and settings look like a feed.

## 6. Component vocabulary

These exist. Reach for one before inventing anything.

| Component | For | Never |
|---|---|---|
| `.card` | One item in a list | Nesting cards |
| `.sheet` | Detail, focused sub-task | Anything permanent |
| `.seg` | 2–3 sibling views | More than 3 on a phone |
| `.chip` / `.chips` | Filters, types, subjects | Actions |
| `.banner` | State that needs saying — offline, queued, error | Success (use a toast) |
| `.pill` / `.tag` | Read-only status on a card | Tappable things |
| `.fab` | The one primary verb of the surface | A second FAB |
| `.empty` | Zero state — and it teaches the feature | A bare "no results" |
| toast | Confirming something that worked | Errors needing a decision |

Empty states carry weight in an app this dense: they are where a feature gets
explained, and they cost nothing when the list is full.

## 7. Settled rules

Decided already, usually the expensive way. Do not relitigate without a
measurement.

- **Pitch black, painted glass.** `--fill` + `--sheen` + `--rim`. No
  `backdrop-filter`, no `blur()`, no `will-change`, no view transitions — one
  full-screen blur per visible row is what made the list stutter.
- **No swipe gestures.** Tried for both navigation and row actions, removed.
  Everything is a visible target.
- **Sheets slide in and out.** `--sheet-ease` in, `--sheet-exit` out, 220ms.
  Closed by the bottom Close button or the back gesture. No cross.
- **Optimistic first.** Write to IndexedDB, render, then sync. The next screen
  is already correct.
- **One-handed.** Primary actions in the bottom third; the top bar holds
  identity and settings, never the main verb.
- **Targets:** 44px standard, 38px in dense toolbars, 34px for chips.
- **Wide content scrolls itself.** Tables, code, diagrams and maths each get
  their own `overflow-x` container. The page body never scrolls sideways.
- **`content-visibility: auto`** on long lists.

## 8. Anti-patterns

- **A hamburger drawer.** Hidden is unused, and the top-left corner is the
  hardest place to reach one-handed.
- **Tabs inside tabs inside tabs.** One level of segments under a surface. If
  you need two, the surface is wrong.
- **A tab per noun.** The corpus is one thing; slicing it into tabs is the
  schema leaking into the interface.
- **Modal dialogs.** Only for destructive confirmation. Everything else is a
  sheet.
- **Badge counts everywhere.** They train you to ignore badges. Reserve them
  for things that expire — review, conflicts.
- **A settings tab.** Visited monthly; a permanent slot is 20% of the tab bar
  for 1% of the use.

## 9. Getting there incrementally

This is a bigger change than any one slice, and it does not need a rewrite.
Each step stands alone and leaves the app working.

| Step | Change | Risk |
|---|---|---|
| D1 | **Library** = merge Search and Notes. Search already returns mixed types with subject chips; add type chips and an empty-query browse. | Low — mostly deletion |
| D2 | **Now** = new home. Ships with Tasks (T1), plus today's events, review and conflicts moved in. | Medium — new surface |
| D3 | Mail → Library chip. Calendar → Now's Month segment. Retire the Calendar tab. | Low |
| D4 | Settings → top-bar gear. Capture → FAB sheet with Note / Task. | Low |
| D5 | **Ask** arrives with Phase 5B. Tab bar reaches its final three. | — |

D1 is worth doing first regardless of Tasks: it removes a tab, deletes code,
and makes the corpus feel like one thing — which is the argument the whole
guide rests on.
