<script>
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { isSignedIn } from '$lib/api.js';
	import { authState } from '$lib/auth.js';
	import { cache } from '$lib/store.js';
	import { state as sync, run } from '$lib/sync.svelte.js';

	let notes = $state([]);
	let loading = $state(true);

	async function load() {
		notes = (await cache.all()).sort((a, b) =>
			(b.updated_at || '').localeCompare(a.updated_at || '')
		);
		loading = false;
	}

	onMount(async () => {
		const s = await authState().catch(() => ({ signed_in: false }));
		if (!s.signed_in && !isSignedIn()) {
			goto('/settings');
			return;
		}
		await load();
		await run();
		await load();
	});

	$effect(() => {
		void sync.lastSync;
		if (!loading) void load();
	});

	const preview = (b) => (b || '').replace(/[#*`[\]]/g, '').slice(0, 140);
	function when(iso) {
		if (!iso) return '';
		const d = new Date(iso),
			m = (Date.now() - d) / 60000;
		if (m < 1) return 'now';
		if (m < 60) return `${Math.floor(m)}m`;
		if (m < 1440) return `${Math.floor(m / 60)}h`;
		if (m < 10080) return `${Math.floor(m / 1440)}d`;
		return d.toLocaleDateString(undefined, { day: 'numeric', month: 'short' });
	}
</script>

<div class="topbar">
	<h1>Notes</h1>
	{#if sync.syncing}<span class="spinner"></span>{/if}
	{#if sync.pending}<span class="pill warn">{sync.pending}</span>{/if}
</div>

{#if sync.conflicts}
	<a class="banner warn" href="/conflicts" style="display:block;text-decoration:none">
		{sync.conflicts} conflict{sync.conflicts > 1 ? 's' : ''} to review →
	</a>
{/if}

{#if loading}
	<div class="empty">Loading…</div>
{:else if notes.length === 0}
	<div class="empty">
		<p style="font-size:17px;margin-bottom:6px;color:var(--text)">Nothing here yet</p>
		<p>Tap <strong>Capture</strong> to write your first note.</p>
	</div>
{:else}
	{#each notes as n, i (n.slug)}
		<a
			class="card"
			class:pending={n._dirty}
			href="/note/{n.slug}"
			style="animation-delay:{Math.min(i * 22, 160)}ms"
		>
			<div class="row">
				<h3>{n.title}</h3>
				<span class="spacer"></span>
				{#if n._dirty}<span class="pill">saving</span>{/if}
			</div>
			{#if preview(n.body)}<p>{preview(n.body)}</p>{/if}
			<div class="row" style="margin-top:7px">
				{#each n.tags || [] as t}<span class="tag">{t}</span>{/each}
				<span class="spacer"></span>
				<span class="muted">{when(n.updated_at)}</span>
			</div>
		</a>
	{/each}
{/if}

<button class="fab" onclick={() => goto('/new')} aria-label="New note">+</button>
