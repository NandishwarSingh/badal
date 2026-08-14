<script>
	// Add or edit one timetable slot. Same bottom-sheet chrome and offline-safe
	// save path as TaskSheet: try the server, fall back to the outbox.
	import { Timetable } from '$lib/api.js';
	import { timetableCache } from '$lib/store.js';
	import { queueSlot, queueSlotDelete } from '$lib/sync.svelte.js';
	import { toast } from '$lib/toast.svelte.js';
	import { DAYS } from '$lib/timetime.js';

	let { slot, onclose, onsaved, ondeleted } = $props();

	const EXIT_MS = 220;
	const isNew = !slot?.id;
	let closing = $state(false);
	let busy = $state(false);
	let err = $state('');

	let title = $state(slot?.title || '');
	let kind = $state(slot?.kind || 'class');
	let day = $state(slot?.day_of_week ?? 0);
	let location = $state(slot?.location || '');
	let starts = $state(minToTime(slot?.starts_min ?? 540)); // default 09:00
	let ends = $state(minToTime(slot?.ends_min ?? 590)); // default 09:50

	function minToTime(min) {
		const m = ((min % 1440) + 1440) % 1440;
		return `${String(Math.floor(m / 60)).padStart(2, '0')}:${String(m % 60).padStart(2, '0')}`;
	}
	function timeToMin(v) {
		const [h, m] = (v || '0:0').split(':').map(Number);
		return h * 60 + m;
	}

	function close() {
		if (closing) return;
		closing = true;
		setTimeout(() => onclose?.(), EXIT_MS);
	}

	$effect(() => {
		history.pushState({ sheet: true }, '');
		const onPop = () => close();
		window.addEventListener('popstate', onPop);
		return () => {
			window.removeEventListener('popstate', onPop);
			if (history.state?.sheet) history.back();
		};
	});

	// An end at or before the start is read as crossing midnight, so a
	// 21:00–01:00 block is stored as 1260→1500 rather than rejected.
	const crossesMidnight = $derived(timeToMin(ends) <= timeToMin(starts));

	async function save() {
		if (!title.trim()) {
			err = 'A title is required.';
			return;
		}
		let endMin = timeToMin(ends);
		if (endMin <= timeToMin(starts)) endMin += 1440;
		busy = true;
		err = '';
		const payload = {
			id: slot?.id || crypto.randomUUID(),
			kind,
			title: title.trim(),
			location: location.trim(),
			day_of_week: Number(day),
			starts_min: timeToMin(starts),
			ends_min: endMin,
			notes: slot?.notes || '',
			version: slot?.version
		};
		try {
			const res = await Timetable.putSlot(payload);
			await timetableCache.put({ ...res.slot, _dirty: false });
			toast('Saved');
			onsaved?.(res.slot);
		} catch (e) {
			if (e?.status === 409) {
				err = 'Changed on another device — close and reopen to see it.';
				busy = false;
				return;
			}
			// Offline or server down: queue it, the outbox finishes later.
			await queueSlot(payload);
			toast('Queued');
			onsaved?.(payload);
		} finally {
			busy = false;
		}
	}

	async function remove() {
		if (!confirm('Delete this slot?')) return;
		try {
			await Timetable.delSlot(slot.id);
		} catch {
			await queueSlotDelete(slot.id);
		}
		ondeleted?.(slot.id);
	}
</script>

<div
	class="sheet-scrim"
	class:closing
	role="button"
	tabindex="-1"
	onclick={close}
	onkeydown={(k) => k.key === 'Escape' && close()}
></div>

<div class="sheet" class:closing role="dialog" aria-modal="true">
	<h2>{isNew ? 'New block' : 'Block'}</h2>

	<label class="fieldlabel" for="s-title">Title</label>
	<input id="s-title" bind:value={title} placeholder="e.g. Machine Learning, or A1 · PAMAT501" />

	<label class="fieldlabel" for="s-kind">Type</label>
	<div class="seg" id="s-kind">
		<button class:on={kind === 'class'} onclick={() => (kind = 'class')}>Class</button>
		<button class:on={kind === 'study'} onclick={() => (kind = 'study')}>Study</button>
	</div>

	<label class="fieldlabel">Day</label>
	<div class="daypick">
		{#each DAYS as d, i}
			<button class="chip" class:on={day === i} onclick={() => (day = i)}>{d}</button>
		{/each}
	</div>

	<div class="timerow">
		<div>
			<label class="fieldlabel" for="s-start">Start</label>
			<input id="s-start" type="time" bind:value={starts} />
		</div>
		<div>
			<label class="fieldlabel" for="s-end">End</label>
			<input id="s-end" type="time" bind:value={ends} />
		</div>
	</div>
	{#if crossesMidnight}
		<p class="muted" style="margin:6px 0 0;font-size:12px">Runs past midnight into the next morning.</p>
	{/if}

	<label class="fieldlabel" for="s-loc">Location <span class="muted">(optional)</span></label>
	<input id="s-loc" bind:value={location} placeholder="Room, campus, home…" />

	{#if err}<div class="banner err" style="margin-top:10px">{err}</div>{/if}

	<div class="resolve" style="margin-top:14px">
		{#if !isNew}
			<button class="danger" onclick={remove} disabled={busy}>Delete</button>
		{/if}
		<span class="spacer"></span>
		<button class="primary" onclick={save} disabled={busy || !title.trim()}>
			{#if busy}<span class="spinner"></span>{/if} Save
		</button>
	</div>

	<div class="sheet-actions">
		<button class="sheet-close" onclick={close}>Close</button>
	</div>
</div>

<style>
	.daypick { display: flex; gap: 6px; flex-wrap: wrap; }
	.timerow { display: flex; gap: 12px; }
	.timerow > div { flex: 1; }
	.timerow input { width: 100%; }
</style>
