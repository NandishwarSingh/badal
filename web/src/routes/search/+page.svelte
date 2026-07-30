<script>
	import { Notes } from '$lib/api.js';
	import { cache } from '$lib/store.js';
	import { state as sync } from '$lib/sync.svelte.js';

	let q = $state('');
	let hits = $state([]);
	let searched = $state(false);
	let timer;

	function onInput() {
		clearTimeout(timer);
		timer = setTimeout(go, 250);
	}

	async function go() {
		if (!q.trim()) { hits = []; searched = false; return; }
		searched = true;
		if (sync.online) {
			try {
				const res = await Notes.search(q);
				hits = (res.hits || []).map((h) => ({ ...h.note, snippet: h.snippet }));
				return;
			} catch { /* fall through to local */ }
		}
		// Offline: substring match over the cache. Worse than Postgres
		// full-text, but it beats an error message on a train.
		const needle = q.toLowerCase();
		hits = (await cache.all())
			.filter((n) => (n.title + ' ' + n.body).toLowerCase().includes(needle))
			.slice(0, 30);
	}
</script>

<div class="topbar"><h1>Search</h1></div>

<!-- svelte-ignore a11y_autofocus -->
<input bind:value={q} oninput={onInput} placeholder="Search notes…" autofocus
	type="search" enterkeyhint="search" onkeydown={(e) => e.key === 'Enter' && go()} />

{#if !sync.online && searched}
	<p class="muted" style="margin:10px 2px">Offline — searching cached notes only.</p>
{/if}

<div style="margin-top:12px">
	{#if searched && hits.length === 0}
		<div class="empty">No matches for “{q}”.</div>
	{/if}
	{#each hits as n, i (n.slug)}
		<a class="card" href="/note/{n.slug}">
			<h3>{n.title}</h3>
			{#if n.snippet}<p>{@html n.snippet}</p>{:else}<p>{(n.body || '').slice(0, 130)}</p>{/if}
		</a>
	{/each}
</div>
