<script>
	import Skeleton from '$lib/Skeleton.svelte';
	import Icon from '$lib/Icon.svelte';
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

	// Collapse the newlines too: a raw body dropped into one line shows the
	// gaps between paragraphs as a run of spaces.
	const preview = (b) =>
		(b || '')
			.replace(/[#*`[\]]/g, '')
			.replace(/\s+/g, ' ')
			.trim()
			.slice(0, 130);
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

<div class="pagehead">
	<h1>
		Notes
		<span class="sub">
			{notes.length} note{notes.length === 1 ? '' : 's'}{#if sync.pending} · {sync.pending} queued{/if}
		</span>
	</h1>
	<span class="spacer"></span>
	{#if sync.syncing}<span class="spinner"></span>{/if}
	<button class="iconbtn" onclick={() => goto('/new')} aria-label="New note">
		<svg viewBox="0 0 24 24"><path d="M12 5v14 M5 12h14" /></svg>
	</button>
</div>

{#if sync.conflicts}
	<a class="banner warn" href="/conflicts" style="display:block;text-decoration:none">
		{sync.conflicts} conflict{sync.conflicts > 1 ? 's' : ''} to review →
	</a>
{/if}

{#if loading}
	<Skeleton rows={5} />
{:else if notes.length === 0}
	<div class="empty">
		<p style="font-size:17px;margin-bottom:6px;color:var(--text)">Nothing here yet</p>
		<p>Tap <strong>Capture</strong> to write your first note.</p>
	</div>
{:else}
	<div class="group">
		{#each notes as n (n.slug)}
			<a class="grow rich noicon" href="/note/{n.slug}">
				<span class="gbody">
					<span class="gtitle">{n.title}</span>
					<!-- The preview is most of what makes a note findable by eye;
					     a list of bare titles all look alike after a week. -->
					{#if preview(n.body)}<span class="gtext">{preview(n.body)}</span>{/if}
					<span class="gmeta">
						<span>{when(n.updated_at)}</span>
						{#each n.tags || [] as t}<span class="dot">·</span><span>{t}</span>{/each}
						{#if n._dirty}<span class="pill">saving</span>{/if}
					</span>
				</span>
			</a>
		{/each}
	</div>
{/if}
