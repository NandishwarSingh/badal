<script>
	import { Cal, Mail, api } from '$lib/api.js';
	import { toast } from '$lib/toast.svelte.js';

	let { event, onclose, onchanged } = $props();

	// Closing plays the reverse of the open animation before the component is
	// torn down. Unmounting immediately makes the sheet vanish, which reads as
	// a glitch next to the slide-up it arrived with.
	const EXIT_MS = 220;
	let closing = $state(false);

	function close() {
		if (closing) return;
		closing = true;
		setTimeout(() => onclose?.(), EXIT_MS);
	}

	// Close on the hardware/gesture back button. On Android that is the first
	// thing anyone tries, and on iOS the back-swipe should not leave the page
	// with a sheet still open over it.
	$effect(() => {
		history.pushState({ sheet: true }, '');
		const onPop = () => close();
		window.addEventListener('popstate', onPop);
		return () => {
			window.removeEventListener('popstate', onPop);
			// If we are closing for any other reason, drop the entry we added.
			if (history.state?.sheet) history.back();
		};
	});


	let e = $state({ ...event });
	let editing = $state(false);
	let busy = $state('');
	let err = $state('');
	let source = $state(null);
	let showSource = $state(false);

	// Local datetime strings for the inputs. <input type="datetime-local"> has
	// no timezone, so convert both ways against the browser's zone rather than
	// letting an ISO string with a Z silently shift the time by 5.5 hours.
	const toLocalInput = (iso) => {
		if (!iso) return '';
		const d = new Date(iso);
		const off = d.getTimezoneOffset() * 60000;
		return new Date(d - off).toISOString().slice(0, 16);
	};
	const fromLocalInput = (s) => (s ? new Date(s).toISOString() : null);

	let fTitle = $state(event.title);
	let fStart = $state(toLocalInput(event.starts_at));
	let fEnd = $state(toLocalInput(event.ends_at));
	let fLoc = $state(event.location || '');
	let fKind = $state(event.kind);
	let fNotes = $state(event.notes || '');

	const kinds = ['event', 'deadline', 'exam', 'class', 'meeting', 'task'];

	async function loadSource() {
		showSource = !showSource;
		if (source || !e.source_message) return;
		try {
			source = await api(`/v1/mail/messages/${e.source_message}`);
		} catch (x) {
			err = x.message;
		}
	}

	async function setState(state) {
		busy = state;
		try {
			const out = await Cal.setState(e.id, state);
			toast(state === 'confirmed' ? 'Added to calendar' : 'Dismissed');
			onchanged?.(out, state);
			close();
		} catch (x) {
			err = x.message;
		} finally {
			busy = '';
		}
	}

	async function save() {
		busy = 'save';
		try {
			const out = await Cal.patch(e.id, {
				title: fTitle,
				starts_at: fromLocalInput(fStart),
				ends_at: fEnd ? fromLocalInput(fEnd) : null,
				location: fLoc,
				kind: fKind,
				notes: fNotes
			});
			e = out;
			editing = false;
			toast('Saved');
			onchanged?.(out, out.state);
		} catch (x) {
			err = x.message;
		} finally {
			busy = '';
		}
	}

	async function remove() {
		if (!confirm('Delete this event?')) return;
		busy = 'del';
		try {
			await Cal.remove(e.id);
			toast('Deleted');
			onchanged?.(e, 'deleted');
			close();
		} catch (x) {
			err = x.message;
		} finally {
			busy = '';
		}
	}

	const when = (ev) => {
		const d = new Date(ev.starts_at);
		const day = d.toLocaleDateString(undefined, {
			weekday: 'long', day: 'numeric', month: 'long', year: 'numeric'
		});
		if (ev.all_day) return `${day} · all day`;
		const t = d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });
		if (!ev.ends_at) return `${day} · ${t}`;
		const t2 = new Date(ev.ends_at).toLocaleTimeString(undefined, {
			hour: '2-digit', minute: '2-digit'
		});
		return `${day} · ${t}–${t2}`;
	};
</script>

<!-- Backdrop closes; the sheet itself stops propagation. -->
<div
	class="sheet-scrim"
	role="button"
	tabindex="-1"
	class:closing
	onclick={close}
	onkeydown={(k) => k.key === 'Escape' && close()}
></div>

<div class="sheet" class:closing role="dialog" aria-modal="true">
	{#if !editing}
		<div class="row" style="align-items:flex-start">
			<h2>{e.title}</h2>
			<span class="spacer"></span>
			<span class="pill {e.state === 'proposed' ? 'warn' : 'ok'}">{e.state}</span>
		</div>

		<div class="sheet-when">{when(e)}</div>
		{#if e.location}<div class="muted" style="margin-top:2px">📍 {e.location}</div>{/if}

		<div class="row" style="margin-top:10px">
			<span class="tag">{e.kind}</span>
			{#if e.confidence > 0}
				<span class="pill">confidence {Math.round(e.confidence * 100)}%</span>
			{/if}
		</div>

		{#if e.notes}<p style="margin-top:11px;font-size:14px">{e.notes}</p>{/if}

		{#if e.evidence}
			<h4 class="sheet-h">Why this is here</h4>
			<div class="evidence">“{e.evidence}”</div>
			<p class="muted" style="font-size:11.5px;margin:5px 0 0">
				Quoted from the email and verified present in it before this event was created.
			</p>
		{/if}

		{#if e.source_message}
			<h4 class="sheet-h">Source</h4>
			<button class="srcbtn" onclick={loadSource}>
				<div style="min-width:0;flex:1;text-align:left">
					<div style="font-size:13.5px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">
						{e.source_subject || 'email'}
					</div>
					<div class="muted" style="font-size:12px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">
						{e.source_from}
					</div>
				</div>
				<span class="muted">{showSource ? '▲' : '▼'}</span>
			</button>
			{#if showSource}
				{#if source}
					<pre class="srcbody">{source.body_text}</pre>
				{:else}
					<p class="muted" style="padding:8px 2px">Loading…</p>
				{/if}
			{/if}
		{/if}

		{#if err}<div class="banner err" style="margin-top:11px">{err}</div>{/if}

		<div class="sheet-actions">
			{#if e.state === 'proposed'}
				<button class="primary" onclick={() => setState('confirmed')} disabled={!!busy}>
					{#if busy === 'confirmed'}<span class="spinner"></span>{/if} Add to calendar
				</button>
				<button onclick={() => setState('rejected')} disabled={!!busy}>Dismiss</button>
			{:else}
				<button onclick={() => setState('proposed')} disabled={!!busy}>Un-confirm</button>
			{/if}
			<div class="row" style="gap:8px">
				<button style="flex:1" onclick={() => (editing = true)} disabled={!!busy}>Edit</button>
				<button class="danger" onclick={remove} disabled={!!busy}>Delete</button>
			</div>
			<button class="sheet-close" onclick={close}>Close</button>
		</div>
	{:else}
		<h2 style="margin-bottom:12px">Edit event</h2>
		<label class="fld"><span>Title</span><input bind:value={fTitle} /></label>
		<label class="fld"><span>Starts</span><input type="datetime-local" bind:value={fStart} /></label>
		<label class="fld"><span>Ends</span><input type="datetime-local" bind:value={fEnd} /></label>
		<label class="fld"><span>Location</span><input bind:value={fLoc} /></label>
		<label class="fld">
			<span>Kind</span>
			<select bind:value={fKind}>
				{#each kinds as k}<option value={k}>{k}</option>{/each}
			</select>
		</label>
		<label class="fld"><span>Notes</span><textarea rows="3" bind:value={fNotes}></textarea></label>
		{#if err}<div class="banner err">{err}</div>{/if}
		<div class="sheet-actions">
			<button class="primary" onclick={save} disabled={busy === 'save' || !fTitle.trim()}>
				{#if busy === 'save'}<span class="spinner"></span>{/if} Save
			</button>
			<button onclick={() => (editing = false)} disabled={!!busy}>Cancel</button>
			<button class="sheet-close" onclick={close}>Close</button>
		</div>
	{/if}
</div>
