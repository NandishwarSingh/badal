<script>
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { Notes } from '$lib/api.js';
	import { state as sync } from '$lib/sync.svelte.js';

	let list = $state([]);
	let loading = $state(true);

	onMount(async () => {
		try {
			const res = await Notes.conflicts();
			list = res.conflicts || [];
			sync.conflicts = list.length;
		} finally { loading = false; }
	});
</script>

<div class="topbar">
	<button class="ghost" onclick={() => goto('/')} style="padding:6px 10px">‹</button>
	<h1>Conflicts</h1>
</div>

{#if loading}
	<div class="empty">Loading…</div>
{:else if list.length === 0}
	<div class="empty">Nothing to resolve.</div>
{:else}
	<p class="muted" style="margin:0 2px 12px">
		Two devices edited the same note while offline. Both versions were kept — open each,
		copy across whatever you want, then delete the copy.
	</p>
	{#each list as n (n.slug)}
		<a class="card" href="/note/{n.slug}">
			<h3>{n.title}</h3>
			<p>{(n.body || '').slice(0, 160)}</p>
		</a>
	{/each}
{/if}
