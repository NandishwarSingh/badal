<script>
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { api } from '$lib/api.js';
	import { state as sync } from '$lib/sync.svelte.js';

	let items = $state([]);
	let loading = $state(true);

	onMount(async () => {
		try {
			const [c, n] = await Promise.all([api('/v1/conflicts'), api('/v1/notes')]);
			const bySlug = new Map((n.notes || []).map((x) => [x.id, x]));
			items = (c.conflicts || []).map((x) => ({ ...x, note: bySlug.get(x.note_id) }));
			sync.conflicts = items.length;
		} finally {
			loading = false;
		}
	});
</script>

<div class="pagehead">
	<button class="iconbtn" onclick={() => goto('/notes')} aria-label="Back">
		<svg viewBox="0 0 24 24"><path d="M15 5l-7 7 7 7" /></svg>
	</button>
	<h1 style="font-size:24px">Conflicts</h1>
</div>

{#if loading}
	<div class="empty">Loading…</div>
{:else if items.length === 0}
	<div class="empty">Nothing to resolve.</div>
{:else}
	<p class="muted" style="margin:0 2px 12px">
		Two devices edited the same note. Open each to see what changed and pick a side.
	</p>
	<div class="cards">
	{#each items as c (c.id)}
		<a class="card" href="/note/{c.note?.slug ?? ''}">
			<div class="row">
				<h3>{c.note?.title ?? c.incoming_title}</h3>
				<span class="spacer"></span>
				<span class="pill warn">v{c.base_version} → v{c.server_version}</span>
			</div>
			<p>{(c.incoming_body || '').slice(0, 130)}</p>
		</a>
	{/each}
	</div>
{/if}
