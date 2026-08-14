<script>
	// Today's timetable: classes and study blocks for the current weekday, with
	// the block happening right now lifted out and a live countdown to the next.
	//
	// The clock ticks while the page is open (badal already polls in the
	// foreground), so "in 25 min" stays honest without a reload.
	import { onMount } from 'svelte';
	import {
		DAYS_LONG, nowIn, hhmm, dur, until, statusOf, slotsForDay, nextSlot
	} from '$lib/timetime.js';

	let { slots = [], tz = 'Asia/Kolkata', onedit } = $props();

	// One reactive clock, advanced each half-minute. Everything below derives
	// from it, so a single tick re-renders the strip.
	let clock = $state(nowIn(tz));
	onMount(() => {
		const t = setInterval(() => (clock = nowIn(tz)), 30_000);
		const onVis = () => {
			if (document.visibilityState === 'visible') clock = nowIn(tz);
		};
		document.addEventListener('visibilitychange', onVis);
		return () => {
			clearInterval(t);
			document.removeEventListener('visibilitychange', onVis);
		};
	});

	const today = $derived(slotsForDay(slots, clock.dow));
	const next = $derived(nextSlot(slots, clock.dow, clock.min));
	const withStatus = $derived(
		today.map((s) => ({ ...s, status: statusOf(s, clock.dow, clock.min) }))
	);
	const current = $derived(withStatus.find((s) => s.status === 'now') || null);
	// Progress through the current block, for the thin under-bar.
	const progress = $derived.by(() => {
		if (!current) return 0;
		const end = current.ends_min > 1440 && current.day_of_week !== clock.dow
			? current.ends_min - 1440 : current.ends_min;
		const start = current.day_of_week !== clock.dow ? 0 : current.starts_min;
		const span = end - start || 1;
		return Math.max(0, Math.min(1, (clock.min - start) / span));
	});
</script>

<div class="tt">
	<div class="tt-head">
		<span class="tt-day">{DAYS_LONG[clock.dow]}</span>
		<span class="tt-now">{hhmm(clock.min)}</span>
	</div>

	{#if today.length === 0}
		<div class="tt-empty">
			<p>No classes today.</p>
			<span class="muted">A free day — the week view shows what's coming.</span>
		</div>
	{:else}
		{#if current}
			<button class="tt-current" onclick={() => onedit?.(current)}>
				<div class="tt-cur-top">
					<span class="tt-badge">Now</span>
					<span class="tt-cur-title">{current.title}</span>
				</div>
				<div class="tt-cur-meta">
					<span class="tt-time">{hhmm(current.starts_min)}–{hhmm(current.ends_min)}</span>
					{#if current.location}<span class="tt-loc">{current.location}</span>{/if}
					<span class="tt-kind {current.kind}">{current.kind}</span>
				</div>
				<div class="tt-progress"><span style="width:{progress * 100}%"></span></div>
			</button>
		{/if}

		{#if next && (!current || next.slot.day_of_week === clock.dow)}
			<div class="tt-next">
				<span class="tt-next-lbl">Next</span>
				<span class="tt-next-title">{next.slot.title}</span>
				<span class="tt-next-in">{until(next.inMin)}</span>
			</div>
		{/if}

		<div class="tt-list">
			{#each withStatus as s (s.id)}
				<button
					class="tt-row {s.status}"
					onclick={() => onedit?.(s)}
					aria-label="{s.title}, {hhmm(s.starts_min)} to {hhmm(s.ends_min)}"
				>
					<span class="tt-rail {s.kind}" class:on={s.status === 'now'}></span>
					<span class="tt-clock">
						<span class="tt-s">{hhmm(s.starts_min)}</span>
						<span class="tt-e">{hhmm(s.ends_min)}</span>
					</span>
					<span class="tt-body">
						<span class="tt-title">{s.title}</span>
						<span class="tt-sub">
							<span class="tt-kind {s.kind}">{s.kind}</span>
							{#if s.location}<span class="tt-loc">{s.location}</span>{/if}
							<span class="tt-dur">{dur(s.ends_min - s.starts_min)}</span>
						</span>
					</span>
				</button>
			{/each}
		</div>
	{/if}
</div>

<style>
	.tt { display: flex; flex-direction: column; }
	.tt-head {
		display: flex; align-items: baseline; justify-content: space-between;
		margin: 2px 0 12px;
	}
	.tt-day { font-size: 17px; font-weight: 640; letter-spacing: -0.01em; }
	.tt-now {
		font-size: 14px; color: var(--dim); font-variant-numeric: tabular-nums;
	}

	.tt-empty { padding: 22px 2px 8px; }
	.tt-empty p { margin: 0 0 3px; font-size: 15px; }

	/* The one block happening now: the only surface allowed a fill, so the eye
	   lands on it. Everything else stays flat, per the design system. */
	.tt-current {
		display: block; width: 100%; text-align: left; border: none;
		background: var(--accent-soft); color: var(--text);
		border-radius: var(--r); padding: 13px 15px 11px; margin-bottom: 12px;
		box-shadow: var(--rim);
	}
	.tt-cur-top { display: flex; align-items: center; gap: 9px; }
	.tt-badge {
		font-size: 10.5px; font-weight: 700; letter-spacing: 0.08em;
		text-transform: uppercase; color: var(--accent);
		border: 1px solid var(--accent); border-radius: 999px; padding: 1px 7px;
	}
	.tt-cur-title { font-size: 16.5px; font-weight: 650; line-height: 1.25; }
	.tt-cur-meta {
		display: flex; align-items: center; gap: 10px; margin-top: 6px;
		font-size: 12.5px; color: var(--muted); flex-wrap: wrap;
	}
	.tt-time { font-variant-numeric: tabular-nums; }
	.tt-progress {
		height: 3px; border-radius: 2px; background: var(--edge);
		margin-top: 10px; overflow: hidden;
	}
	.tt-progress span {
		display: block; height: 100%; background: var(--accent);
		transition: width 0.4s var(--ease);
	}

	.tt-next {
		display: flex; align-items: baseline; gap: 9px; padding: 0 2px 14px;
		font-size: 13px; color: var(--muted);
	}
	.tt-next-lbl {
		font-size: 10.5px; font-weight: 700; letter-spacing: 0.07em;
		text-transform: uppercase; color: var(--dim);
	}
	.tt-next-title { font-weight: 560; color: var(--text); }
	.tt-next-in {
		margin-left: auto; color: var(--dim); font-variant-numeric: tabular-nums;
	}

	/* Unboxed rows: a hairline between, a coloured rail for the type, and the
	   clock in a tabular gutter so times line up down the column. */
	.tt-list {
		display: flex; flex-direction: column;
		margin: 0 calc(var(--pad, 15px) * -1);
		border-top: 1px solid var(--edge);
	}
	.tt-row {
		display: flex; align-items: stretch; gap: 12px; width: 100%;
		text-align: left; border: none; background: none; color: var(--text);
		border-bottom: 1px solid var(--edge);
		padding: 11px var(--pad, 15px); border-radius: 0;
		transition: background-color 0.14s var(--ease);
	}
	.tt-row:active { background-color: var(--fill-hi); }
	.tt-row.past { opacity: 0.44; }
	.tt-row.now { background-color: color-mix(in srgb, var(--accent-soft) 60%, transparent); }

	.tt-rail {
		flex: 0 0 3px; border-radius: 2px; background: var(--muted); align-self: stretch;
	}
	.tt-rail.study { background: var(--warn); }
	.tt-rail.class { background: var(--accent); }
	.tt-rail.on { box-shadow: 0 0 0 1px var(--accent); }

	.tt-clock {
		flex: 0 0 auto; display: flex; flex-direction: column; gap: 1px;
		font-variant-numeric: tabular-nums; min-width: 44px;
	}
	.tt-clock .tt-s { font-size: 13.5px; font-weight: 580; }
	.tt-clock .tt-e { font-size: 11.5px; color: var(--dim); }

	.tt-body { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 3px; justify-content: center; }
	.tt-title {
		font-size: 15px; font-weight: 560; line-height: 1.28; overflow-wrap: anywhere;
	}
	.tt-sub { display: flex; align-items: center; gap: 8px; font-size: 12px; color: var(--muted); }
	.tt-dur { color: var(--dim); font-variant-numeric: tabular-nums; }

	.tt-kind {
		font-size: 10.5px; font-weight: 650; letter-spacing: 0.03em;
		text-transform: uppercase;
	}
	.tt-kind.study { color: var(--warn); }
	.tt-kind.class { color: var(--accent); }
	.tt-loc {
		font-size: 11.5px; color: var(--muted);
	}
</style>
