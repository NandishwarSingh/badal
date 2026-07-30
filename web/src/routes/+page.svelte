<script>
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { isSignedIn } from '$lib/api.js';
	import { authState } from '$lib/auth.js';
	import { cache } from '$lib/store.js';
	import { state as sync, run, queuePut, queueDelete } from '$lib/sync.svelte.js';
	import { toast, toastAction } from '$lib/toast.svelte.js';
	import { swipe } from '$lib/swipe.js';

	let notes = $state([]);
	let loading = $state(true);

	async function load() {
		const all = await cache.all();
		// Pinned first, then most recently touched.
		notes = all.sort((a, b) => {
			const pa = (a.tags || []).includes('pinned') ? 1 : 0;
			const pb = (b.tags || []).includes('pinned') ? 1 : 0;
			if (pa !== pb) return pb - pa;
			return (b.updated_at || '').localeCompare(a.updated_at || '');
		});
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

	async function del(n) {
		// Vanish immediately; the queued delete only fires if undo lapses.
		notes = notes.filter((x) => x.slug !== n.slug);
		let undone = false;
		toastAction(`Deleted “${n.title}”`, 'Undo', () => {
			undone = true;
			void load();
		});
		setTimeout(async () => {
			if (undone) return;
			await queueDelete(n.slug);
			await load();
		}, 5000);
	}

	async function togglePin(n) {
		const tags = new Set(n.tags || []);
		const pinning = !tags.has('pinned');
		if (pinning) tags.add('pinned');
		else tags.delete('pinned');
		await queuePut({
			slug: n.slug,
			title: n.title,
			body: n.body,
			tags: [...tags],
			version: n.version
		});
		toast(pinning ? 'Pinned' : 'Unpinned');
		await load();
	}

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
		<div class="swipe-row" style="animation-delay:{Math.min(i * 35, 320)}ms">
			<div class="swipe-bg pin">
				<svg viewBox="0 0 24 24"><path d="M12 17v5 M9 3h6l-1 7 3 3v2H7v-2l3-3z" /></svg>
				{(n.tags || []).includes('pinned') ? 'Unpin' : 'Pin'}
			</div>
			<div class="swipe-bg del">
				Delete
				<svg viewBox="0 0 24 24"
					><path d="M4 7h16 M10 11v6 M14 11v6 M6 7l1 13h10l1-13 M9 7V4h6v3" /></svg
				>
			</div>
			<a
				class="card"
				class:pending={n._dirty}
				href="/note/{n.slug}"
				use:swipe={{ onLeft: () => del(n), onRight: () => togglePin(n) }}
			>
				<div class="row">
					<h3>{n.title}</h3>
					<span class="spacer"></span>
					{#if (n.tags || []).includes('pinned')}<span class="pill warn">pinned</span>{/if}
					{#if n._dirty}<span class="pill warn">saving</span>{/if}
				</div>
				{#if preview(n.body)}<p>{preview(n.body)}</p>{/if}
				<div class="row" style="margin-top:7px">
					{#each (n.tags || []).filter((t) => t !== 'pinned') as t}<span class="tag">{t}</span>{/each}
					<span class="spacer"></span>
					<span class="muted">{when(n.updated_at)}</span>
				</div>
			</a>
		</div>
	{/each}
	<p class="muted" style="text-align:center;margin:18px 0 0;font-size:12px">
		Swipe a note — left to delete, right to pin
	</p>
{/if}

<button class="fab" onclick={() => goto('/new')} aria-label="New note">+</button>
