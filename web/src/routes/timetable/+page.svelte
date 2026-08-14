<script>
	// The timetable, on its own page. Reuses the calendar's agenda row so it
	// reads exactly like the rest of badal: a tabular time column, the title,
	// a muted subline. No cards, no pills, no rails.
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { Timetable as TimetableApi } from '$lib/api.js';
	import { timetableCache } from '$lib/store.js';
	import SlotSheet from '$lib/SlotSheet.svelte';
	import {
		DAYS_LONG, nowIn, hhmm, statusOf, slotsForDay, nextSlot
	} from '$lib/timetime.js';

	let slots = $state([]);
	let tz = $state('Asia/Kolkata');
	let mode = $state('today'); // today | week
	let sheet = $state(null);
	let clock = $state(nowIn('Asia/Kolkata'));

	async function load() {
		const local = await timetableCache.all();
		if (local.length) slots = local;
		try {
			const res = await TimetableApi.get();
			slots = res.slots || [];
			tz = res.timetable?.tz || tz;
			clock = nowIn(tz);
			await timetableCache.replaceAll(slots);
		} catch {
			// Offline: the cache is already on screen.
		}
	}

	onMount(() => {
		load();
		const t = setInterval(() => (clock = nowIn(tz)), 30_000);
		return () => clearInterval(t);
	});

	const today = $derived(
		slotsForDay(slots, clock.dow).map((s) => ({ ...s, status: statusOf(s, clock.dow, clock.min) }))
	);
	const current = $derived(today.find((s) => s.status === 'now') || null);
	const later = $derived(today.filter((s) => s.status === 'upcoming'));
	const next = $derived(nextSlot(slots, clock.dow, clock.min));

	const byDay = $derived.by(() => {
		const out = Array.from({ length: 7 }, () => []);
		for (const s of slots) out[s.day_of_week]?.push(s);
		for (const d of out) d.sort((a, b) => a.starts_min - b.starts_min);
		return out;
	});

	const sub = (s) => (s.location ? `${s.kind} · ${s.location}` : s.kind);
	function addSlot() {
		sheet = { day_of_week: clock.dow, kind: 'class' };
	}
</script>

<div class="pagehead">
	<button class="iconbtn" onclick={() => goto('/')} aria-label="Back">
		<svg viewBox="0 0 24 24"><path d="M15 5l-7 7 7 7" /></svg>
	</button>
	<h1>Timetable</h1>
	<span class="spacer"></span>
	<button class="iconbtn" onclick={addSlot} aria-label="Add a block">
		<svg viewBox="0 0 24 24"><path d="M12 5v14 M5 12h14" /></svg>
	</button>
</div>

<div class="seg">
	<button class:on={mode === 'today'} onclick={() => (mode = 'today')}>Today</button>
	<button class:on={mode === 'week'} onclick={() => (mode = 'week')}>Week</button>
</div>

{#if slots.length === 0}
	<div class="empty">
		<p>No timetable yet.</p>
		<span class="muted">Tap + to add your first class or study block.</span>
	</div>
{:else if mode === 'today'}
	<p class="ttlabel">Now</p>
	{#if current}
		<button class="grow ev" onclick={() => (sheet = current)}>
			<span class="ev-time">{hhmm(current.starts_min)}</span>
			<span class="gbody">
				<span class="gtitle">{current.title}</span>
				<span class="gtext">{sub(current)} · until {hhmm(current.ends_min)}</span>
			</span>
		</button>
	{:else}
		<p class="muted ttnone">
			Nothing on right now.{#if next && next.slot.day_of_week === clock.dow}
				Next is {next.slot.title} at {hhmm(next.slot.starts_min)}.{/if}
		</p>
	{/if}

	<p class="ttlabel">Later today</p>
	{#if later.length}
		<div class="group">
			{#each later as s (s.id)}
				<button class="grow ev" onclick={() => (sheet = s)}>
					<span class="ev-time">{hhmm(s.starts_min)}</span>
					<span class="gbody">
						<span class="gtitle">{s.title}</span>
						<span class="gtext">{sub(s)}</span>
					</span>
				</button>
			{/each}
		</div>
	{:else}
		<p class="muted ttnone">Nothing else today.</p>
	{/if}
{:else}
	{#each byDay as day, dow (dow)}
		{#if day.length}
			<p class="ttlabel" class:on={dow === clock.dow}>
				{DAYS_LONG[dow]}{#if dow === clock.dow} · today{/if}
			</p>
			<div class="group">
				{#each day as s (s.id)}
					{@const isNow = dow === clock.dow && statusOf(s, clock.dow, clock.min) === 'now'}
					<button class="grow ev" class:isnow={isNow} onclick={() => (sheet = s)}>
						<span class="ev-time">{hhmm(s.starts_min)}</span>
						<span class="gbody">
							<span class="gtitle">{s.title}</span>
							<span class="gtext">{sub(s)}{#if isNow} · now{/if}</span>
						</span>
					</button>
				{/each}
			</div>
		{/if}
	{/each}
{/if}

{#if sheet}
	<SlotSheet
		slot={sheet}
		onclose={() => (sheet = null)}
		onsaved={() => {
			sheet = null;
			load();
		}}
		ondeleted={(id) => {
			sheet = null;
			slots = slots.filter((x) => x.id !== id);
		}}
	/>
{/if}

<style>
	/* One small label style; everything else is a global class. */
	.ttlabel {
		font-size: 11px;
		text-transform: uppercase;
		letter-spacing: 0.08em;
		color: var(--dim);
		font-weight: 700;
		margin: 20px 0 4px;
	}
	.ttlabel.on {
		color: var(--accent);
	}
	.ttnone {
		padding: 6px 2px 2px;
		font-size: 13px;
	}
	.ev.isnow .gtitle {
		color: var(--accent);
	}
</style>
