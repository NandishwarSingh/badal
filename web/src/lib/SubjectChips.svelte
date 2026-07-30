<script>
	import { Subjects } from '$lib/api.js';

	// selected is a bound array of slugs. Multi-select is a union, matching
	// what a row of chips looks like it does.
	let { selected = $bindable([]), onchange } = $props();

	let list = $state([]);
	let err = $state('');

	$effect(() => {
		Subjects.list()
			.then((r) => (list = r.subjects || []))
			.catch((e) => (err = e.message || ''));
	});

	function toggle(slug) {
		selected = selected.includes(slug)
			? selected.filter((s) => s !== slug)
			: [...selected, slug];
		onchange?.(selected);
	}

	function clear() {
		selected = [];
		onchange?.(selected);
	}
</script>

{#if list.length}
	<div class="chipbar">
		<div class="chips" role="group" aria-label="Filter by subject">
			{#each list as s (s.slug)}
				<button
					class="chip"
					class:on={selected.includes(s.slug)}
					class:empty={s.count === 0}
					aria-pressed={selected.includes(s.slug)}
					onclick={() => toggle(s.slug)}
				>
					{s.label}
					{#if s.count}<span class="n">{s.count}</span>{/if}
				</button>
			{/each}
		</div>
	</div>
	{#if selected.length}
		<div class="row" style="margin:-4px 0 11px">
			<button class="clear" onclick={clear}>Clear filter</button>
		</div>
	{/if}
{:else if err}
	<div class="banner warn">Subjects unavailable — {err}</div>
{/if}
