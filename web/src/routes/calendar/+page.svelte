<script>
	import { onMount } from 'svelte';
	import { Cal, Mail, Jobs } from '$lib/api.js';
	import { toast } from '$lib/toast.svelte.js';
	import EventSheet from '$lib/EventSheet.svelte';
	import MessageSheet from '$lib/MessageSheet.svelte';
	import { page } from '$app/state';
	import { replaceState } from '$app/navigation';

	let view = $state('calendar'); // calendar | mail | review
	let loading = $state(true);
	let err = $state('');

	let events = $state([]);
	let proposed = $state([]);
	let messages = $state([]);
	let counts = $state({ proposed: 0, confirmed: 0 });

	let cursor = $state(new Date()); // month being shown
	let selected = $state(startOfDay(new Date()));
	let busy = $state('');
	let sheet = $state(null);
	let jobs = $state(null);
	let mailSheet = $state(null);

	function startOfDay(d) {
		const x = new Date(d);
		x.setHours(0, 0, 0, 0);
		return x;
	}
	const iso = (d) => d.toISOString();
	const key = (d) =>
		`${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;

	async function loadMonth() {
		const from = new Date(cursor.getFullYear(), cursor.getMonth(), 1);
		from.setDate(from.getDate() - 7);
		const to = new Date(cursor.getFullYear(), cursor.getMonth() + 1, 1);
		to.setDate(to.getDate() + 14);
		const res = await Cal.range(iso(from), iso(to));
		events = res.events || [];
		counts = res.counts || counts;
	}

	async function loadAll() {
		loading = true;
		err = '';
		try {
			await loadMonth();
			const [p, m] = await Promise.all([
				Cal.proposed().catch(() => ({ events: [] })),
				Mail.messages(60).catch(() => ({ messages: [] }))
			]);
			proposed = p.events || [];
			messages = m.messages || [];
			jobs = await Jobs.stats().catch(() => null);
		} catch (e) {
			err = e.message || 'Could not load.';
		} finally {
			loading = false;
		}
	}

	onMount(async () => {
		await loadAll();
		// Deep links from search: ?event=<id> opens that event, ?mail=<id> opens
		// the message. Without this a search result could only switch tabs.
		const q = page.url.searchParams;
		const ev = q.get('event');
		const ml = q.get('mail');
		if (ml) {
			view = 'mail';
			mailSheet = ml;
		} else if (ev) {
			// May be outside the loaded month, so fall back to fetching it.
			const found =
				events.find((e) => e.id === ev) ||
				proposed.find((e) => e.id === ev) ||
				(await Cal.get(ev).catch(() => null));
			if (found) {
				sheet = found;
				// Jump the calendar to the month the event is in, or opening it
				// leaves the grid showing an unrelated month behind the sheet.
				cursor = new Date(found.starts_at);
				selected = startOfDay(new Date(found.starts_at));
				await loadMonth();
			}
		}
		if (ev || ml) replaceState('/calendar', {});
	});

	// Events grouped by local day. Doing this once beats filtering per cell.
	const byDay = $derived.by(() => {
		const m = new Map();
		for (const e of events) {
			const k = key(new Date(e.starts_at));
			if (!m.has(k)) m.set(k, []);
			m.get(k).push(e);
		}
		return m;
	});

	const weeks = $derived.by(() => {
		const first = new Date(cursor.getFullYear(), cursor.getMonth(), 1);
		const start = new Date(first);
		start.setDate(1 - ((first.getDay() + 6) % 7)); // weeks start Monday
		const out = [];
		for (let w = 0; w < 6; w++) {
			const row = [];
			for (let d = 0; d < 7; d++) {
				const day = new Date(start);
				day.setDate(start.getDate() + w * 7 + d);
				row.push(day);
			}
			out.push(row);
		}
		return out;
	});

	const dayEvents = $derived(byDay.get(key(selected)) || []);
	const today = startOfDay(new Date());

	function shiftMonth(n) {
		cursor = new Date(cursor.getFullYear(), cursor.getMonth() + n, 1);
		loadMonth();
	}

	async function setState(e, state) {
		busy = e.id;
		try {
			const out = await Cal.setState(e.id, state);
			if (state === 'rejected') {
				proposed = proposed.filter((x) => x.id !== e.id);
				events = events.filter((x) => x.id !== e.id);
			} else if (state === 'proposed') {
				proposed = proposed.some((x) => x.id === e.id) ? proposed : [...proposed, out];
				events = events.map((x) => (x.id === e.id ? out : x));
			} else {
				proposed = proposed.filter((x) => x.id !== e.id);
				events = events.map((x) => (x.id === e.id ? out : x));
			}
			counts = { ...counts, proposed: proposed.length };
			toast(state === 'confirmed' ? 'Added to calendar' : 'Dismissed');
		} catch (x) {
			err = x.message;
		} finally {
			busy = '';
		}
	}

	async function confirmAll() {
		busy = 'all';
		try {
			const r = await Cal.confirmAll();
			toast(`${r.confirmed} added`);
			proposed = [];
			await loadAll();
		} catch (x) {
			err = x.message;
		} finally {
			busy = '';
		}
	}

	async function syncMail() {
		busy = 'sync';
		try {
			await Mail.sync();
			const r = await Mail.extract(10);
			toast(r.events ? `${r.events} new event${r.events > 1 ? 's' : ''}` : 'No new events');
			await loadAll();
		} catch (x) {
			err = x.message;
		} finally {
			busy = '';
		}
	}

	const fmtTime = (e) =>
		e.all_day
			? 'All day'
			: new Date(e.starts_at).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });
	const fmtDay = (d) =>
		d.toLocaleDateString(undefined, { weekday: 'long', day: 'numeric', month: 'long' });
	const fmtWhen = (iso2) =>
		new Date(iso2).toLocaleString(undefined, {
			day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit'
		});
</script>

<div class="topbar">
	<h1>{view === 'mail' ? 'Mail' : view === 'review' ? 'Review' : 'Calendar'}</h1>
	<button class="ghost" onclick={syncMail} disabled={!!busy} style="padding:6px 10px;min-height:0">
		{#if busy === 'sync'}<span class="spinner"></span>{:else}↻{/if}
	</button>
</div>

<div class="seg">
	<button class:on={view === 'calendar'} onclick={() => (view = 'calendar')}>Calendar</button>
	<button class:on={view === 'mail'} onclick={() => (view = 'mail')}>Mail</button>
	<button class:on={view === 'review'} onclick={() => (view = 'review')}>
		Review{#if counts.proposed}<span class="segbadge">{counts.proposed}</span>{/if}
	</button>
</div>

{#if err}<div class="banner err">{err}</div>{/if}

<!-- Surfacing the queue is the whole mitigation for a worker that is only
     sometimes there: waiting is fine, silence is not. -->
{#if jobs && (jobs.queued || jobs.leased)}
	<div class="banner info">
		{#if jobs.leased}
			Reading {jobs.leased} image{jobs.leased > 1 ? 's' : ''} on your Mac…
		{:else if jobs.online}
			{jobs.queued} image{jobs.queued > 1 ? 's' : ''} queued — your Mac is picking them up.
		{:else}
			{jobs.queued} image{jobs.queued > 1 ? 's' : ''} waiting for your Mac. They will be read
			when it wakes, or sent to the cloud if it stays asleep.
		{/if}
	</div>
{/if}

{#if loading}
	<div class="empty">Loading…</div>

{:else if view === 'calendar'}
	<div class="calwrap">
	<div class="calcol">
	<div class="calhead">
		<button class="ghost" onclick={() => shiftMonth(-1)} aria-label="Previous month">‹</button>
		<strong>{cursor.toLocaleDateString(undefined, { month: 'long', year: 'numeric' })}</strong>
		<button class="ghost" onclick={() => shiftMonth(1)} aria-label="Next month">›</button>
	</div>

	<div class="grid glass">
		{#each ['M', 'T', 'W', 'T', 'F', 'S', 'S'] as d}
			<div class="dow">{d}</div>
		{/each}
		{#each weeks as week}
			{#each week as day}
				{@const evs = byDay.get(key(day)) || []}
				{@const other = day.getMonth() !== cursor.getMonth()}
				<button
					class="day"
					class:other
					class:today={key(day) === key(today)}
					class:sel={key(day) === key(selected)}
					onclick={() => (selected = startOfDay(day))}
				>
					<span>{day.getDate()}</span>
					{#if evs.length}
						<span class="dots">
							{#each evs.slice(0, 3) as e}
								<i class="dot {e.state === 'proposed' ? 'prop' : e.kind}"></i>
							{/each}
						</span>
					{/if}
				</button>
			{/each}
		{/each}
	</div>

	</div>
	<div class="calcol agenda">
	<h3 class="agenda-h">{fmtDay(selected)}</h3>
	{#if dayEvents.length === 0}
		<p class="muted" style="padding:8px 2px">Nothing scheduled.</p>
	{:else}
		{#each dayEvents as e (e.id)}
			<button class="ev" onclick={() => (sheet = e)}>
				<div class="ev-time">{fmtTime(e)}</div>
				<div class="ev-body">
					<div class="row">
						<strong>{e.title}</strong>
						<span class="spacer"></span>
						<span class="pill {e.state === 'proposed' ? 'warn' : ''}">{e.kind}</span>
					</div>
					{#if e.location}<div class="muted">{e.location}</div>{/if}
					{#if e.source_subject}
						<div class="muted" style="font-size:12px;margin-top:3px">from “{e.source_subject}”</div>
					{/if}
				</div>
			</button>
		{/each}
	{/if}
		</div>
	</div>

{:else if view === 'mail'}
	{#if messages.length === 0}
		<div class="empty">No mail yet.</div>
	{:else}
		{#each messages as m (m.id)}
			<button class="card" style="min-height:0;width:100%;text-align:left"
				onclick={() => (mailSheet = m.id)}>
				<div class="row">
					<strong style="font-size:15px">{m.from_name || m.from_addr}</strong>
					<span class="spacer"></span>
					<span class="muted" style="font-size:12px">{fmtWhen(m.received_at)}</span>
				</div>
				<div style="margin:3px 0 4px;font-size:14px">{m.subject || '(no subject)'}</div>
				<p style="font-size:13px">{m.snippet}</p>
				{#if !m.extracted}
					<span class="pill warn" style="margin-top:6px;display:inline-block">not scanned</span>
				{/if}
			</button>
		{/each}
	{/if}

{:else}
	{#if proposed.length === 0}
		<div class="empty">Nothing to review.</div>
	{:else}
		<div class="row" style="margin-bottom:12px">
			<span class="muted">{proposed.length} found in your mail</span>
			<span class="spacer"></span>
			<button onclick={confirmAll} disabled={!!busy} style="min-height:38px;padding:8px 14px;font-size:14px">
				{#if busy === 'all'}<span class="spinner"></span>{/if} Add all
			</button>
		</div>
		{#each proposed as e (e.id)}
			<div class="card" style="min-height:0">
				<div class="row" role="button" tabindex="0" style="cursor:pointer"
					onclick={() => (sheet = e)} onkeydown={(k) => k.key === 'Enter' && (sheet = e)}>
					<strong>{e.title}</strong>
					<span class="spacer"></span>
					<span class="pill">{e.kind}</span>
				</div>
				<div class="muted" style="margin-top:3px">
					{new Date(e.starts_at).toLocaleString(undefined, {
						weekday: 'short', day: 'numeric', month: 'short',
						...(e.all_day ? {} : { hour: '2-digit', minute: '2-digit' })
					})}{e.location ? ` · ${e.location}` : ''}
				</div>
				<!-- Provenance: the quote the model had to find, and where. This is
				     what makes an accept/dismiss decision possible without opening
				     the original email. -->
				{#if e.evidence}
					<div class="evidence">“{e.evidence}”</div>
				{/if}
				{#if e.source_subject}
					<div class="muted" style="font-size:12px">
						{e.source_from} — {e.source_subject}
					</div>
				{/if}
				<div class="row" style="margin-top:10px;gap:7px">
					<button onclick={() => setState(e, 'confirmed')} disabled={busy === e.id}
						style="flex:1;min-height:40px;font-size:14px">Add</button>
					<button class="ghost" onclick={() => setState(e, 'rejected')} disabled={busy === e.id}
						style="min-height:40px;font-size:14px">Dismiss</button>
				</div>
			</div>
		{/each}
	{/if}
{/if}

{#if mailSheet}
	<MessageSheet id={mailSheet} onclose={() => (mailSheet = null)} />
{/if}

{#if sheet}
	<EventSheet
		event={sheet}
		onclose={() => (sheet = null)}
		onchanged={(ev, state) => {
			if (state === 'rejected' || state === 'deleted') {
				proposed = proposed.filter((x) => x.id !== ev.id);
				events = events.filter((x) => x.id !== ev.id);
			} else if (state === 'proposed') {
				// Un-confirming must put it back in Review immediately. The old
				// handler removed from `proposed` for every state, so the badge
				// only caught up on the next refresh.
				proposed = proposed.some((x) => x.id === ev.id)
					? proposed.map((x) => (x.id === ev.id ? ev : x))
					: [...proposed, ev].sort((a, b) => a.starts_at.localeCompare(b.starts_at));
				events = events.map((x) => (x.id === ev.id ? ev : x));
			} else {
				proposed = proposed.filter((x) => x.id !== ev.id);
				events = events.map((x) => (x.id === ev.id ? ev : x));
			}
			counts = { ...counts, proposed: proposed.length };
		}}
	/>
{/if}
