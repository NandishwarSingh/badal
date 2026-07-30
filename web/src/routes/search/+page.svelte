<script>
	import { Search, Subjects } from '$lib/api.js';
	import { mailSnippet } from '$lib/mailtext.js';
	import { cache } from '$lib/store.js';
	import { state as sync } from '$lib/sync.svelte.js';
	import SubjectChips from '$lib/SubjectChips.svelte';

	let q = $state('');
	let mode = $state('hybrid'); // semantic | keyword
	let subs = $state([]);
	let hits = $state([]);
	let searched = $state(false);
	let browsing = $state(false);
	let busy = $state(false);
	let err = $state('');
	let timer;

	function onInput() {
		clearTimeout(timer);
		// Semantic costs an embedding call per query, so wait a little longer
		// than a local keyword search would.
		timer = setTimeout(go, mode === 'hybrid' ? 420 : 220);
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
			const res = await Search.all(q, mode, 25, subs);
			hits = res.hits || [];
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

	function setMode(m) {
		mode = m;
		if (q.trim()) go();
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
	const icon = { note: 'note', message: 'mail', event: 'event' };

	let labels = $state({});
	$effect(() => {
		Subjects.list()
			.then((r) => {
				const m = {};
				for (const s of r.subjects || []) m[s.slug] = s.label;
				labels = m;
			})
			.catch(() => {});
	});
</script>

<div class="topbar">
	<h1>Search</h1>
	{#if busy}<span class="spinner"></span>{/if}
</div>

<!-- svelte-ignore a11y_autofocus -->
<input
	bind:value={q}
	oninput={onInput}
	placeholder="Search everything…"
	autofocus
	type="search"
	enterkeyhint="search"
	onkeydown={(e) => e.key === 'Enter' && go()}
/>

<div class="seg" style="margin-top:11px">
	<button class:on={mode === 'hybrid'} onclick={() => setMode('hybrid')}>Smart</button>
	<button class:on={mode === 'keyword'} onclick={() => setMode('keyword')}>Exact words</button>
</div>

<SubjectChips bind:selected={subs} onchange={go} />

{#if !sync.online && searched}
	<div class="banner warn">Offline — searching cached notes only.</div>
{/if}
{#if err}<div class="banner err">{err}</div>{/if}

{#if searched && hits.length === 0 && !busy}
	<div class="empty">
		{#if browsing}
			Nothing tagged that yet.
		{:else}
			No matches for “{q}”.
			{#if mode === 'hybrid'}<br /><span class="muted">Try exact words instead.</span>{/if}
		{/if}
	</div>
{/if}

{#if browsing && hits.length}
	<div class="muted" style="margin:-2px 0 10px">{hits.length} tagged items</div>
{/if}

<div class="cards">
	{#each hits as h, i (h.source_type + (h.source_id ?? h.slug) + i)}
		<a class="card" href={href(h)}>
			<div class="row">
				<span class="tag">{icon[h.source_type] ?? h.source_type}</span>
				<span class="spacer"></span>
				{#if h.score}<span class="pill">{Math.round(h.score * 100)}%</span>{/if}
			</div>
			<h3 style="margin-top:7px">{h.title || '(untitled)'}</h3>
			{#if h.snippet}<p>{mailSnippet(h.snippet, 170)}</p>{/if}
			{#if h.subjects?.length}
				<div class="subjs">
					{#each h.subjects as s}<span class="subj">{labels[s] ?? s}</span>{/each}
				</div>
			{/if}
		</a>
	{/each}
</div>
