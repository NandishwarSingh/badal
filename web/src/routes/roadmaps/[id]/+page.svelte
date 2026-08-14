<script>
	// One roadmap: the checklist. Items arrive flat with a parent and a depth;
	// this rebuilds the tree and ticks leaves optimistically.
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { Roadmaps } from '$lib/api.js';
	import { meta } from '$lib/store.js';
	import { toast } from '$lib/toast.svelte.js';
	import RoadmapNode from '$lib/RoadmapNode.svelte';

	const id = page.params.id;
	let rm = $state(null);
	let items = $state([]);
	let loading = $state(true);
	let err = $state('');

	async function load() {
		const cached = await meta.get('roadmap:' + id, null);
		if (cached) {
			rm = cached;
			items = cached.items || [];
			loading = false;
		}
		try {
			const res = await Roadmaps.get(id);
			rm = res.roadmap;
			items = res.roadmap?.items || [];
			await meta.set('roadmap:' + id, res.roadmap);
		} catch (e) {
			if (!cached) err = e?.message || 'Could not load.';
		} finally {
			loading = false;
		}
	}
	onMount(load);

	// Rebuild the tree from the flat list, and roll leaf counts up to topics so
	// each heading can show its own progress.
	const roots = $derived.by(() => {
		const byId = new Map();
		for (const it of items) byId.set(it.id, { ...it, children: [], done: 0, leaves: 0 });
		const top = [];
		for (const it of items) {
			const n = byId.get(it.id);
			if (it.parent_id && byId.has(it.parent_id)) byId.get(it.parent_id).children.push(n);
			else top.push(n);
		}
		const roll = (n) => {
			if (n.kind === 'leaf') {
				n.leaves = 1;
				n.done = n.state === 'done' ? 1 : 0;
			} else {
				for (const c of n.children) {
					roll(c);
					n.leaves += c.leaves;
					n.done += c.done;
				}
			}
			return n;
		};
		top.forEach(roll);
		return top;
	});

	const leaves = $derived(items.filter((i) => i.kind === 'leaf').length);
	const done = $derived(items.filter((i) => i.kind === 'leaf' && i.state === 'done').length);
	const pct = $derived(leaves ? Math.round((done / leaves) * 100) : 0);

	async function tick(itemId) {
		const it = items.find((x) => x.id === itemId);
		if (!it) return;
		const next = it.state === 'done' ? 'todo' : 'done';
		items = items.map((x) => (x.id === itemId ? { ...x, state: next } : x));
		try {
			await Roadmaps.itemState(id, itemId, next);
		} catch {
			// Only a genuinely failed save reverts the tick.
			items = items.map((x) => (x.id === itemId ? { ...x, state: it.state } : x));
			toast('Could not save — try again online');
			return;
		}
		// Cache is best-effort and must never revert the UI. $state.snapshot
		// unwraps the reactive proxy, which IndexedDB cannot structured-clone.
		meta.set('roadmap:' + id, $state.snapshot({ ...rm, items })).catch(() => {});
	}

	async function removeRoadmap() {
		if (!confirm('Delete this roadmap and its checklist?')) return;
		try {
			await Roadmaps.del(id);
		} catch (e) {
			if (e?.status !== 404) {
				toast('Could not delete — try again online');
				return;
			}
		}
		await meta.set('roadmap:' + id, null).catch(() => {});
		toast('Roadmap deleted');
		goto('/roadmaps');
	}
</script>

<div class="pagehead">
	<button class="iconbtn" onclick={() => goto('/roadmaps')} aria-label="Back">
		<svg viewBox="0 0 24 24"><path d="M15 5l-7 7 7 7" /></svg>
	</button>
	<h1>
		{rm?.title || 'Roadmap'}
		{#if rm?.status === 'ready'}<span class="sub">{done}/{leaves} · {pct}%</span>{/if}
	</h1>
	<span class="spacer"></span>
	{#if rm}
		<button class="iconbtn" onclick={removeRoadmap} aria-label="Delete roadmap">
			<svg viewBox="0 0 24 24"><path d="M5 7h14 M9 7V5h6v2 M8 7l1 13h6l1-13" /></svg>
		</button>
	{/if}
</div>

{#if rm?.status === 'ready' && leaves}
	<div class="rm-bar"><span style="width:{pct}%"></span></div>
{/if}

{#if loading && !rm}
	<p class="muted" style="padding:12px 2px">Loading…</p>
{:else if err}
	<div class="banner err">{err}</div>
{:else if rm?.status === 'processing'}
	<div class="empty"><p>Building…</p><span class="muted">Parsing the documents and writing the checklist.</span></div>
{:else if rm?.status === 'failed'}
	<div class="empty"><p>Couldn't build this roadmap.</p><span class="muted">{rm.error || 'The documents had no readable text.'}</span></div>
{:else}
	<div class="rtree">
		{#each roots as n (n.id)}
			<RoadmapNode node={n} ontick={tick} />
		{/each}
	</div>
{/if}

<style>
	.rm-bar {
		height: 4px; border-radius: 2px; background: var(--edge);
		overflow: hidden; margin: 2px 0 8px;
	}
	.rm-bar span { display: block; height: 100%; background: var(--accent); transition: width 0.3s var(--ease); }
	.rtree { display: flex; flex-direction: column; margin-top: 4px; }
</style>
