<script>
	// Placeholder rows shaped like the rows they stand in for.
	//
	// A spinner says "something is happening". This says what is about to be
	// there, and because it occupies the same height the content does, nothing
	// jumps when the real rows arrive.
	let { rows = 4, kind = 'rich' } = $props();
	// Varied widths: a column of identical bars reads as a graphic, not as
	// text about to load.
	const widths = ['72%', '54%', '81%', '63%', '77%', '48%'];
</script>

<div class="group {kind === 'rich' ? 'marked' : ''}" aria-hidden="true">
	{#each Array(rows) as _, i}
		<div class="skrow">
			{#if kind === 'tile'}
				<span class="sktile"></span>
			{:else}
				<span class="skmark"></span>
			{/if}
			<span class="skbody">
				<span class="sk" style="width:{widths[i % widths.length]}"></span>
				{#if kind === 'rich'}
					<span class="sk" style="width:{i % 2 ? '88%' : '95%'};height:10px"></span>
				{/if}
			</span>
		</div>
	{/each}
</div>
