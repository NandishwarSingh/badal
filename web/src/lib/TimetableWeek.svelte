<script>
	// The whole week, stacked day by day. A vertical list reads on a phone
	// without the sideways scroll a 7-column grid forces, and centres cleanly
	// on desktop. Today's heading is lifted and the current block is marked.
	import { onMount } from 'svelte';
	import { DAYS_LONG, nowIn, hhmm, dur, statusOf } from '$lib/timetime.js';

	let { slots = [], tz = 'Asia/Kolkata', onedit } = $props();

	let clock = $state(nowIn(tz));
	onMount(() => {
		const t = setInterval(() => (clock = nowIn(tz)), 30_000);
		return () => clearInterval(t);
	});

	const byDay = $derived.by(() => {
		const out = Array.from({ length: 7 }, () => []);
		for (const s of slots) out[s.day_of_week]?.push(s);
		for (const day of out) day.sort((a, b) => a.starts_min - b.starts_min);
		return out;
	});
	const hoursFor = (day) =>
		day.reduce((n, s) => n + (s.ends_min - s.starts_min), 0);
</script>

<div class="wk">
	{#each byDay as day, dow (dow)}
		<section class="wk-day" class:today={dow === clock.dow}>
			<header class="wk-head">
				<span class="wk-name">{DAYS_LONG[dow]}</span>
				{#if dow === clock.dow}<span class="wk-tag">Today</span>{/if}
				<span class="wk-count">
					{day.length ? dur(hoursFor(day)) : 'free'}
				</span>
			</header>

			{#if day.length === 0}
				<p class="wk-none">No classes.</p>
			{:else}
				<div class="wk-list">
					{#each day as s (s.id)}
						{@const st = dow === clock.dow ? statusOf(s, clock.dow, clock.min) : 'later'}
						<button class="wk-row {st}" onclick={() => onedit?.(s)}>
							<span class="wk-rail {s.kind}" class:on={st === 'now'}></span>
							<span class="wk-time">{hhmm(s.starts_min)}<span class="wk-e">{hhmm(s.ends_min)}</span></span>
							<span class="wk-title">{s.title}</span>
							{#if st === 'now'}<span class="wk-live">now</span>{/if}
							<span class="wk-kind {s.kind}"></span>
						</button>
					{/each}
				</div>
			{/if}
		</section>
	{/each}
</div>

<style>
	.wk { display: flex; flex-direction: column; gap: 20px; }
	.wk-day { display: flex; flex-direction: column; }
	.wk-head {
		display: flex; align-items: baseline; gap: 10px;
		padding: 0 2px 7px; border-bottom: 1px solid var(--edge);
	}
	.wk-name { font-size: 15px; font-weight: 640; letter-spacing: -0.01em; }
	.wk-day.today .wk-name { color: var(--accent); }
	.wk-tag {
		font-size: 10px; font-weight: 700; letter-spacing: 0.07em; text-transform: uppercase;
		color: var(--accent); border: 1px solid var(--accent); border-radius: 999px; padding: 0 6px;
	}
	.wk-count {
		margin-left: auto; font-size: 12px; color: var(--dim);
		font-variant-numeric: tabular-nums;
	}
	.wk-none { margin: 9px 2px 2px; font-size: 13px; color: var(--dim); }

	.wk-list { display: flex; flex-direction: column; }
	.wk-row {
		display: flex; align-items: center; gap: 11px; width: 100%;
		text-align: left; border: none; background: none; color: var(--text);
		border-bottom: 1px solid var(--edge); padding: 9px 2px; border-radius: 0;
	}
	.wk-row:last-child { border-bottom: none; }
	.wk-row:active { background-color: var(--fill-hi); }
	.wk-row.past { opacity: 0.5; }
	.wk-row.now { background-color: color-mix(in srgb, var(--accent-soft) 55%, transparent); }

	.wk-rail { flex: 0 0 3px; align-self: stretch; border-radius: 2px; background: var(--muted); }
	.wk-rail.class { background: var(--accent); }
	.wk-rail.study { background: var(--warn); }
	.wk-rail.on { box-shadow: 0 0 0 1px var(--accent); }

	.wk-time {
		flex: 0 0 auto; min-width: 46px; display: flex; flex-direction: column;
		font-size: 12.5px; font-weight: 560; font-variant-numeric: tabular-nums; line-height: 1.3;
	}
	.wk-time .wk-e { font-size: 10.5px; color: var(--dim); font-weight: 400; }
	.wk-title { flex: 1; min-width: 0; font-size: 14px; font-weight: 540; overflow-wrap: anywhere; }
	.wk-live {
		flex: 0 0 auto; font-size: 10px; font-weight: 700; letter-spacing: 0.06em;
		text-transform: uppercase; color: var(--accent);
	}
	.wk-kind { flex: 0 0 6px; height: 6px; border-radius: 999px; background: var(--muted); }
	.wk-kind.class { background: var(--accent); }
	.wk-kind.study { background: var(--warn); }
</style>
