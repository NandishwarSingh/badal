<script>
	import { Search, Subjects } from '$lib/api.js';
	import { mailSnippet } from '$lib/mailtext.js';
	import { cache } from '$lib/store.js';
	import { state as sync } from '$lib/sync.svelte.js';
	import SubjectChips from '$lib/SubjectChips.svelte';

	let q = $state('');
	let subs = $state([]);
	let hits = $state([]);
	let searched = $state(false);
	let browsing = $state(false);
	let busy = $state(false);
	let err = $state('');
	let timer;

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
	// A glyph per type, so the eye can skip to the kind it wants without
	// reading the label on every row.
	const mark = { note: '▤', message: '✉', event: '◇', task: '☑' };

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

<div class="feed">
	{#each hits as h, i (h.source_type + (h.source_id ?? h.slug) + i)}
		<a class="feedrow" href={href(h)}>
			<span class="feedmark {h.source_type}">{mark[h.source_type] ?? '•'}</span>
			<span class="feedmain">
				<span class="feedmeta">
					<span>{icon[h.source_type] ?? h.source_type}</span>
					{#if h.score}<span class="when">{Math.round(h.score * 100)}%</span>{/if}
				</span>
				<span class="feedtitle">{h.title || '(untitled)'}</span>
				{#if h.snippet}<span class="feedsub">{mailSnippet(h.snippet, 150)}</span>{/if}
				{#if h.subjects?.length}
					<span class="subjs">
						{#each h.subjects as sj}<span class="subj">{labels[sj] ?? sj}</span>{/each}
					</span>
				{/if}
			</span>
		</a>
	{/each}
</div>
