<script>
	import { Tasks } from '$lib/api.js';
	import { taskCache } from '$lib/store.js';
	import { queueTask } from '$lib/sync.svelte.js';
	import { toast } from '$lib/toast.svelte.js';

	let { task, onclose, onsaved, ondeleted } = $props();

	const EXIT_MS = 220;
	let closing = $state(false);
	let busy = $state(false);
	let err = $state('');

	let title = $state(task.title || '');
	let notes = $state(task.notes || '');
	let priority = $state(task.priority || 0);
	// <input type="datetime-local"> wants local wall time with no zone, so the
	// stored UTC instant has to be shifted into the browser's offset and back.
	let due = $state(toLocalInput(task.due_at));
	let remind = $state(toLocalInput(firstUnfired(task)));
	let recur = $state(task.recur_every || '');

	function firstUnfired(t) {
		const r = (t.reminders || []).find((x) => !x.fired_at);
		return r?.fire_at || null;
	}

	function toLocalInput(iso) {
		if (!iso) return '';
		const d = new Date(iso);
		const off = d.getTimezoneOffset() * 60000;
		return new Date(d.getTime() - off).toISOString().slice(0, 16);
	}
	const fromLocalInput = (v) => (v ? new Date(v).toISOString() : null);

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

	// Presets do the arithmetic, because picking "tomorrow at 9" out of a
	// datetime field on a phone is six taps for something this common.
	function setDue(kind) {
		const d = new Date();
		d.setSeconds(0, 0);
		if (kind === 'today') d.setHours(18, 0);
		else if (kind === 'tomorrow') {
			d.setDate(d.getDate() + 1);
			d.setHours(9, 0);
		} else if (kind === 'week') {
			d.setDate(d.getDate() + 7);
			d.setHours(9, 0);
		} else {
			due = '';
			return;
		}
		due = toLocalInput(d.toISOString());
	}

	function setRemind(mins) {
		if (mins === null) {
			remind = '';
			return;
		}
		// Relative to the due date when there is one -- "an hour before" is
		// what people mean -- and to now when there is not.
		const base = due ? new Date(due) : new Date();
		remind = toLocalInput(new Date(base.getTime() - mins * 60000).toISOString());
	}

	async function save() {
		if (!title.trim()) {
			err = 'A title is required.';
			return;
		}
		busy = true;
		err = '';
		const dueISO = fromLocalInput(due);
		const payload = {
			id: task.id,
			title: title.trim(),
			notes,
			state: task.state || 'open',
			priority: Number(priority),
			due_at: dueISO,
			// A bare date means all-day; a time typed in means it is not.
			all_day: !dueISO || due.endsWith('T00:00'),
			version: task.version,
			remind_at: remind ? [fromLocalInput(remind)] : [],
			// A repeat has nothing to advance from without a date, and the
			// server rejects the combination rather than guessing.
			recur_every: dueISO ? recur : '',
			recur_interval: 1
		};
		try {
			const res = await Tasks.put(payload);
			await taskCache.put(res.task);
			toast('Saved');
			onsaved?.(res.task);
		} catch (e) {
			if (e?.status === 409) {
				err = 'Changed on another device — close and reopen to see it.';
				busy = false;
				return;
			}
			// Offline or server down: queue it and let the outbox finish later.
			await queueTask({ ...task, ...payload });
			toast('Queued');
			onsaved?.({ ...task, ...payload });
		} finally {
			busy = false;
		}
	}

	async function remove() {
		if (!confirm('Delete this task?')) return;
		ondeleted?.(task.id);
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
	<h2>Task</h2>

	<label class="fieldlabel" for="t-title">Title</label>
	<input id="t-title" bind:value={title} placeholder="What needs doing" />

	<label class="fieldlabel" for="t-due">Due</label>
	<div class="chips presets">
		<button class="chip" onclick={() => setDue('today')}>Today</button>
		<button class="chip" onclick={() => setDue('tomorrow')}>Tomorrow</button>
		<button class="chip" onclick={() => setDue('week')}>Next week</button>
		<button class="chip" class:on={!due} onclick={() => setDue(null)}>No date</button>
	</div>
	<input id="t-due" type="datetime-local" bind:value={due} />

	<label class="fieldlabel" for="t-remind">Remind me</label>
	<div class="chips presets">
		<button class="chip" onclick={() => setRemind(0)} disabled={!due}>At the time</button>
		<button class="chip" onclick={() => setRemind(60)} disabled={!due}>1h before</button>
		<button class="chip" onclick={() => setRemind(1440)} disabled={!due}>1 day before</button>
		<button class="chip" class:on={!remind} onclick={() => setRemind(null)}>None</button>
	</div>
	<input id="t-remind" type="datetime-local" bind:value={remind} />
	{#if remind}
		<p class="muted" style="margin:6px 0 0;font-size:12px">
			Arrives as a notification even with the app closed.
		</p>
	{/if}

	<label class="fieldlabel" for="t-recur">Repeats</label>
	<div class="seg" id="t-recur">
		<button class:on={!recur} onclick={() => (recur = '')}>Never</button>
		<button class:on={recur === 'day'} onclick={() => (recur = 'day')} disabled={!due}>Daily</button>
		<button class:on={recur === 'week'} onclick={() => (recur = 'week')} disabled={!due}>Weekly</button>
		<button class:on={recur === 'month'} onclick={() => (recur = 'month')} disabled={!due}>Monthly</button>
	</div>
	{#if recur && !due}
		<p class="muted" style="margin:6px 0 0;font-size:12px">Set a due date to repeat from.</p>
	{:else if recur}
		<p class="muted" style="margin:6px 0 0;font-size:12px">
			Completing it schedules the next one.
		</p>
	{/if}

	<label class="fieldlabel" for="t-prio">Priority</label>
	<div class="seg" id="t-prio">
		<button class:on={priority == 0} onclick={() => (priority = 0)}>None</button>
		<button class:on={priority == 2} onclick={() => (priority = 2)}>High</button>
		<button class:on={priority == 3} onclick={() => (priority = 3)}>Urgent</button>
	</div>

	<label class="fieldlabel" for="t-notes">Notes</label>
	<textarea id="t-notes" bind:value={notes} rows="4" placeholder="Anything else"></textarea>

	{#if err}<div class="banner err" style="margin-top:10px">{err}</div>{/if}

	<div class="resolve" style="margin-top:14px">
		<button class="danger" onclick={remove} disabled={busy}>Delete</button>
		<span class="spacer"></span>
		<button class="primary" onclick={save} disabled={busy || !title.trim()}>
			{#if busy}<span class="spinner"></span>{/if} Save
		</button>
	</div>

	<div class="sheet-actions">
		<button class="sheet-close" onclick={close}>Close</button>
	</div>
</div>
