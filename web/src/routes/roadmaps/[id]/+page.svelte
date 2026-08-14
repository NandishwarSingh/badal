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
			await meta.set('roadmap:' + id, { ...rm, items });
		} catch {
			items = items.map((x) => (x.id === itemId ? { ...x, state: it.state } : x));
			toast('Could not save — try again online');
		}
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
