<script>
	// One node of a roadmap. Whether it is a section or a checkable item is
	// decided by whether it HAS CHILDREN, never by a stored flag -- so a section
	// always expands to something and every leaf is tickable. This also heals
	// older roadmaps whose stored kind disagreed with their structure.
	import Icon from '$lib/Icon.svelte';
	import Self from '$lib/RoadmapNode.svelte';
	let { node, ontick } = $props();
	const isBranch = $derived((node.children?.length ?? 0) > 0);
	let open = $state(node.depth === 0);
</script>

<div class="rnode" style="padding-left:{Math.min(node.depth, 4) * 15}px">
	{#if isBranch}
		<button class="rtopic" onclick={() => (open = !open)} aria-expanded={open}>
			<span class="rchev" class:open>
				<Icon name="chevron" size={13} stroke={2.2} />
			</span>
			<span class="rtitle">{node.title}</span>
			{#if node.leaves}<span class="rcount">{node.done}/{node.leaves}</span>{/if}
		</button>
		{#if open}
			{#each node.children as c (c.id)}
				<Self node={c} {ontick} />
			{/each}
		{/if}
	{:else}
		<button class="rleaf" class:done={node.state === 'done'} onclick={() => ontick(node.id)}>
			<span class="tick" class:on={node.state === 'done'}>
				{#if node.state === 'done'}
					<svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="3"><path d="M4 12.5l5 5L20 6.5" /></svg>
				{/if}
			</span>
			<span class="rtitle">{node.title}</span>
		</button>
	{/if}
</div>

<style>
	.rnode { display: flex; flex-direction: column; }
	.rtopic,
	.rleaf {
		display: flex; align-items: center; gap: 10px; width: 100%;
		text-align: left; border: none; background: none; color: var(--text);
		padding: 10px 2px; border-bottom: 1px solid var(--edge); border-radius: 0;
	}
	.rtopic:active,
	.rleaf:active { background-color: var(--fill-hi); }
	.rtopic .rtitle { font-weight: 620; }
	.rchev {
		flex: 0 0 auto; color: var(--dim); display: grid; place-items: center;
		transform: rotate(0deg); transition: transform 0.15s var(--ease);
	}
	.rchev.open { transform: rotate(90deg); }
	.rtitle { flex: 1; min-width: 0; font-size: 14.5px; line-height: 1.35; overflow-wrap: anywhere; }
	.rcount {
		flex: 0 0 auto; font-size: 12px; color: var(--dim); font-variant-numeric: tabular-nums;
	}
	.rleaf.done .rtitle { color: var(--muted); text-decoration: line-through; }
</style>
