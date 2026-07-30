<script>
	import { Search } from '$lib/api.js';
	import { cache } from '$lib/store.js';
	import { state as sync } from '$lib/sync.svelte.js';

	let q = $state('');
	let mode = $state('hybrid'); // semantic | keyword
	let hits = $state([]);
	let searched = $state(false);
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
		if (!q.trim()) { hits = []; searched = false; return; }
		searched = true; err = '';
		if (!sync.online) return offline();
		busy = true;
		try {
			const res = await Search.all(q, mode);
			hits = res.hits || [];
		} catch (e) {
			err = e.message || 'Search failed.';
			await offline();
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
			.map((n) => ({ source_type: 'note', title: n.title, snippet: (n.body || '').slice(0, 160), slug: n.slug }));
	}

	function setMode(m) {
		mode = m;
		if (q.trim()) go();
	}

	const href = (h) => (h.source_type === 'note' && h.slug ? `/note/${h.slug}` : '/calendar');
	const icon = { note: 'note', message: 'mail', event: 'event' };
</script>

<div class="topbar">
	<h1>Search</h1>
	{#if busy}<span class="spinner"></span>{/if}
</div>

<!-- svelte-ignore a11y_autofocus -->
<input bind:value={q} oninput={onInput} placeholder="Search everything…" autofocus
	type="search" enterkeyhint="search" onkeydown={(e) => e.key === 'Enter' && go()} />

<div class="seg" style="margin-top:11px">
	<button class:on={mode === 'hybrid'} onclick={() => setMode('hybrid')}>Smart</button>
	<button class:on={mode === 'keyword'} onclick={() => setMode('keyword')}>Exact words</button>
</div>

{#if !sync.online && searched}
	<div class="banner warn">Offline — searching cached notes only.</div>
{/if}
{#if err}<div class="banner err">{err}</div>{/if}

{#if searched && hits.length === 0 && !busy}
	<div class="empty">
		No matches for “{q}”.
		{#if mode === 'hybrid'}<br /><span class="muted">Try exact words instead.</span>{/if}
	</div>
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
			{#if h.snippet}<p>{@html h.snippet}</p>{/if}
		</a>
	{/each}
</div>
