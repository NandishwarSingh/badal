<script>
	import { Search, Subjects } from '$lib/api.js';
	import { mailSnippet } from '$lib/mailtext.js';
	import { cache } from '$lib/store.js';
	import { state as sync } from '$lib/sync.svelte.js';
	import SubjectChips from '$lib/SubjectChips.svelte';
	import Icon from '$lib/Icon.svelte';
	import Skeleton from '$lib/Skeleton.svelte';

	let q = $state('');
	let subs = $state([]);
	let hits = $state([]);
	let searched = $state(false);
	let browsing = $state(false);
	let busy = $state(false);
	let err = $state('');
	let timer;

	// Recent searches, kept locally. An empty search screen is a dead end --
	// this is the one list that is always relevant before you have typed, and
	// re-running a past query is the commonest thing you do here.
	const HIST_KEY = 'badal.searches';
	let history = $state([]);

	function loadHistory() {
		try {
			history = JSON.parse(localStorage.getItem(HIST_KEY) || '[]');
		} catch {
			history = [];
		}
	}

	function remember(term) {
		const t = term.trim();
		if (t.length < 2) return;
		history = [t, ...history.filter((x) => x.toLowerCase() !== t.toLowerCase())].slice(0, 8);
		try {
			localStorage.setItem(HIST_KEY, JSON.stringify(history));
		} catch {
			// Private mode, or the quota is full. Losing history is not worth
			// an error message.
		}
	}

	function forget(term) {
		history = history.filter((x) => x !== term);
		try {
			localStorage.setItem(HIST_KEY, JSON.stringify(history));
		} catch {}
	}

	function rerun(term) {
		q = term;
		go();
	}

	function onInput() {
		clearTimeout(timer);
		// Every query costs an embedding call and a rerank, so wait until the
		// typing actually stops.
		timer = setTimeout(go, 420);
	}

	async function go() {
		// A subject with no query is a browse, not a search: show me everything
		// tagged this, newest first. Without it a chip does nothing until you
		// type, which is not what a filter row looks like it offers.
		if (!q.trim()) {
			if (subs.length) return browse();
			hits = [];
			searched = false;
			browsing = false;
			return;
		}
		searched = true;
		browsing = false;
		err = '';
		if (!sync.online) return offline();
		busy = true;
		try {
			const res = await Search.all(q, 'hybrid', 25, subs);
			hits = res.hits || [];
			// Remembered only once the server answered, so a half-typed word
			// that was never really searched does not end up in the list.
			remember(q);
		} catch (e) {
			err = e.message || 'Search failed.';
			await offline();
		} finally {
			busy = false;
		}
	}

	async function browse() {
		searched = true;
		browsing = true;
		err = '';
		if (!sync.online) return offline();
		busy = true;
		try {
			const res = await Subjects.browse(subs, 60);
			hits = res.hits || [];
		} catch (e) {
			err = e.message || 'Could not load.';
			hits = [];
		} finally {
			busy = false;
		}
	}

	// Offline falls back to a substring scan of cached notes. Worse than either
	// server mode, but better than an error message on a train.
	async function offline() {
		const needle = q.toLowerCase();
		hits = (await cache.all())
			.filter((n) => (n.title + ' ' + n.body).toLowerCase().includes(needle))
			.slice(0, 30)
			.map((n) => ({
				source_type: 'note',
				title: n.title,
				snippet: (n.body || '').slice(0, 160),
				slug: n.slug
			}));
	}

	// Every result type needs somewhere real to land. Sending mail and events
	// to a bare /calendar switched the tab and opened nothing, which is most
	// of the corpus.
	function href(h) {
		if (h.source_type === 'note' && h.slug) return `/note/${h.slug}`;
		if (h.source_type === 'event') return `/calendar?event=${h.source_id}`;
		if (h.source_type === 'message') return `/calendar?mail=${h.source_id}`;
		return '/calendar';
	}
	const icon = { note: 'note', message: 'mail', event: 'event', task: 'task' };
	// An icon per type, so the eye can skip to the kind it wants without
	// reading the label on every row.
	const mark = { note: 'note', message: 'mail', event: 'event', task: 'task' };

	let labels = $state({});
	$effect(() => {
		loadHistory();
		Subjects.list()
			.then((r) => {
				const m = {};
				for (const s of r.subjects || []) m[s.slug] = s.label;
				labels = m;
			})
			.catch(() => {});
	});
</script>

<div class="pagehead">
	<h1>
		Search
		<span class="sub">Ask in your own words — wording need not match</span>
	</h1>
	<span class="spacer"></span>
	{#if busy}<span class="spinner"></span>{/if}
</div>

<!-- svelte-ignore a11y_autofocus -->
<input
	bind:value={q}
	oninput={onInput}
	placeholder="Ask in your own words…"
	autofocus
	type="search"
	enterkeyhint="search"
	onkeydown={(e) => e.key === 'Enter' && go()}
/>

<SubjectChips bind:selected={subs} onchange={go} />

{#if !sync.online && searched}
	<div class="banner warn">Offline — searching cached notes only.</div>
{/if}
{#if err}<div class="banner err">{err}</div>{/if}

<!-- Nothing typed: recent searches rather than a blank screen. -->
{#if !searched && !busy}
	{#if history.length}
		<div class="sectionhead">
			<h2>Recent</h2>
			<span class="spacer"></span>
			<button
				class="linkbtn"
				onclick={() => {
					history = [];
					localStorage.removeItem(HIST_KEY);
				}}>Clear</button
			>
		</div>
		<div class="group">
			{#each history as term}
				<div class="grow histrow">
					<button class="histmain" onclick={() => rerun(term)}>
						<span class="gmark"><Icon name="clock" size={17} /></span>
						<span class="gtitle">{term}</span>
					</button>
					<button class="histdrop" aria-label="Remove {term}" onclick={() => forget(term)}>
						<Icon name="close" size={13} stroke={2.2} />
					</button>
				</div>
			{/each}
		</div>
	{:else}
		<div class="empty">
			<p style="font-size:17px;color:var(--text);margin-bottom:6px">Search everything</p>
			<span class="muted">
				Notes, mail, events and tasks in one place. Describe what you are after — the wording does
				not have to match.
			</span>
		</div>
	{/if}
{/if}

{#if busy && hits.length === 0}
	<Skeleton rows={4} />
{/if}

{#if searched && hits.length === 0 && !busy}
	<div class="empty">
		{#if browsing}
			Nothing tagged that yet.
		{:else}
			No matches for “{q}”.
			<br /><span class="muted">Try describing it differently — wording does not have to match.</span>
		{/if}
	</div>
{/if}

{#if browsing && hits.length}
	<div class="muted" style="margin:-2px 0 10px">{hits.length} tagged items</div>
{/if}

<div class="group marked">
	{#each hits as h, i (h.source_type + (h.source_id ?? h.slug) + i)}
		<a class="grow rich" href={href(h)}>
			<span class="gmark {h.source_type}"><Icon name={mark[h.source_type] ?? 'note'} size={17} /></span>
			<span class="gbody">
				<span class="gmeta">
					<span>{icon[h.source_type] ?? h.source_type}</span>
					{#if h.score}<span class="when">{Math.round(h.score * 100)}%</span>{/if}
				</span>
				<span class="gtitle">{h.title || '(untitled)'}</span>
				{#if h.snippet}<span class="gtext">{mailSnippet(h.snippet, 150)}</span>{/if}
				{#if h.subjects?.length}
					<span class="gsub">
						{#each h.subjects as sj}<span class="subj">{labels[sj] ?? sj}</span>{/each}
					</span>
				{/if}
			</span>
		</a>
	{/each}
</div>
