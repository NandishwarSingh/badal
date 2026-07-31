<script>
	// Now: what needs you today.
	//
	// The home surface from docs/DESIGN.md. Tasks are its content, but the point
	// is the question it answers -- "why did I open this?" -- so everything here
	// is actionable in place. Ticking a task must not navigate anywhere.
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { Tasks, isSignedIn } from '$lib/api.js';
	import { authState } from '$lib/auth.js';
	import { taskCache } from '$lib/store.js';
	import { state as sync, queueTask, queueTaskDelete } from '$lib/sync.svelte.js';
	import { toast } from '$lib/toast.svelte.js';
	import TaskSheet from '$lib/TaskSheet.svelte';

	let view = $state('open'); // open | today | done
	let tasks = $state([]);
	let counts = $state({ open: 0, overdue: 0, today: 0, done: 0 });
	let loading = $state(true);
	let quick = $state('');
	let sheet = $state(null);

	async function load() {
		// Cache first, so the list is on screen before the network answers.
		const local = await taskCache.all();
		if (local.length) tasks = sortLocal(local.filter(matchesView));
		try {
			const res = await Tasks.list(view);
			tasks = res.tasks || [];
			counts = res.counts || counts;
			await taskCache.replaceAll(res.tasks || []);
		} catch {
			// Offline: the cache is already on screen, which is the point.
		} finally {
			loading = false;
		}
	}

	const matchesView = (t) =>
		view === 'done' ? t.state !== 'open' : t.state === 'open' && (view !== 'today' || isToday(t));

	function sortLocal(list) {
		return [...list].sort((a, b) => {
			if (!a.due_at !== !b.due_at) return a.due_at ? -1 : 1;
			if (a.due_at && b.due_at) return a.due_at.localeCompare(b.due_at);
			return (b.priority || 0) - (a.priority || 0);
		});
	}

	function isToday(t) {
		if (!t.due_at) return false;
		const end = new Date();
		end.setHours(23, 59, 59, 999);
		return new Date(t.due_at) <= end;
	}

	onMount(() => {
		if (!isSignedIn() && !authState.session) {
			goto('/settings');
			return;
		}
		load();
		// A notification's Done button acts inside the service worker, so an
		// open page has to be told rather than finding out on the next poll.
		const onMsg = (e) => {
			if (e.data?.type === 'task-changed') load();
		};
		navigator.serviceWorker?.addEventListener('message', onMsg);

		// Deep link from a reminder notification.
		const id = page.url.searchParams.get('task');
		if (id) {
			Tasks.get(id)
				.then((r) => (sheet = r.task))
				.catch(() => {});
		}
		return () => navigator.serviceWorker?.removeEventListener('message', onMsg);
	});

	function setView(v) {
		view = v;
		loading = true;
		load();
	}

	// Optimistic: the row changes the moment you tap. The queue makes that
	// safe -- a failed write is retried, not lost.
	async function toggle(t) {
		const next = t.state === 'open' ? 'done' : 'open';
		tasks = tasks.map((x) => (x.id === t.id ? { ...x, state: next } : x));
		try {
			const res = await Tasks.setState(t.id, next);
			await taskCache.put(res.task);
			setTimeout(load, 260);
		} catch {
			await queueTask({ ...t, state: next, version: undefined });
			toast(sync.online ? 'Saved when back online' : 'Offline — queued');
		}
	}

	async function addQuick() {
		const title = quick.trim();
		if (!title) return;
		quick = '';
		const t = {
			id: crypto.randomUUID(),
			title,
			notes: '',
			state: 'open',
			priority: 0,
			all_day: true,
			reminders: []
		};
		tasks = [...tasks, t];
		await queueTask(t);
		setTimeout(load, 300);
	}

	function relative(iso) {
		if (!iso) return '';
		const d = new Date(iso);
		const now = new Date();
		const day = (x) => new Date(x.getFullYear(), x.getMonth(), x.getDate());
		const days = Math.round((day(d) - day(now)) / 86400000);
		const time = d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });
		if (days < -1) return `${-days} days overdue`;
		if (days === -1) return 'Yesterday';
		if (days === 0) return `Today ${time}`;
		if (days === 1) return `Tomorrow ${time}`;
		if (days < 7) return d.toLocaleDateString(undefined, { weekday: 'long' });
		return d.toLocaleDateString(undefined, { day: 'numeric', month: 'short' });
	}

	const overdue = (t) => t.state === 'open' && t.due_at && new Date(t.due_at) < new Date();
	const hasReminder = (t) => (t.reminders || []).some((r) => !r.fired_at);
</script>

<div class="topbar">
	<h1>Now</h1>
	{#if sync.syncing}<span class="spinner"></span>{/if}
	<span class="spacer"></span>
	<!-- Settings lives here, not in the tab bar: monthly use should not cost a
	     fifth of the bar. See docs/DESIGN.md. -->
	<button
		class="ghost"
		onclick={() => goto('/settings')}
		aria-label="Settings"
		style="padding:6px 10px"
	>
		<!-- The same avatar the You tab used, so the icon means what it always
		     meant; only its position changed. -->
		<svg
			viewBox="0 0 24 24"
			width="21"
			height="21"
			fill="none"
			stroke="currentColor"
			stroke-width="1.85"
			stroke-linecap="round"
			stroke-linejoin="round"
		><path d="M12 12a4 4 0 1 0 0-8 4 4 0 0 0 0 8z M4.5 20a7.5 7.5 0 0 1 15 0" /></svg>
	</button>
</div>

<div class="seg">
	<button class:on={view === 'open'} onclick={() => setView('open')}>All</button>
	<button class:on={view === 'today'} onclick={() => setView('today')}>
		Today{#if counts.overdue}<span class="segbadge">{counts.overdue}</span>{/if}
	</button>
	<button class:on={view === 'done'} onclick={() => setView('done')}>Done</button>
</div>

<form class="quickadd" onsubmit={(e) => (e.preventDefault(), addQuick())}>
	<input bind:value={quick} placeholder="Add a task…" enterkeyhint="done" aria-label="Add a task" />
	<button class="primary" type="submit" disabled={!quick.trim()}>Add</button>
</form>

{#if !sync.online}
	<div class="banner warn">Offline — changes are queued.</div>
{/if}

{#if loading && tasks.length === 0}
	<div class="empty">Loading…</div>
{:else if tasks.length === 0}
	<div class="empty">
		{#if view === 'done'}
			Nothing finished yet.
		{:else if view === 'today'}
			<p>Nothing due today.</p>
			<span class="muted">Anything with a date appears here on the day.</span>
		{:else}
			<p>No tasks.</p>
			<span class="muted">Type above to add one, then tap it to set a date and a reminder.</span>
		{/if}
	</div>
{/if}

<div class="tasklist">
	{#each tasks as t (t.id)}
		<div class="taskrow" class:isdone={t.state !== 'open'}>
			<!-- Ticking is the commonest action on this screen, so it gets the
			     biggest target and never requires aim. -->
			<button
				class="tick"
				class:on={t.state !== 'open'}
				aria-label={t.state === 'open' ? 'Mark done' : 'Mark not done'}
				onclick={() => toggle(t)}
			>
				{#if t.state !== 'open'}
					<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="3">
						<path d="M4 12.5l5 5L20 6.5" />
					</svg>
				{/if}
			</button>

			<button class="taskmain" onclick={() => (sheet = t)}>
				<span class="tasktitle">{t.title}</span>
				{#if t.due_at || hasReminder(t) || t.priority > 1 || t.subjects?.length}
					<span class="taskmeta">
						{#if t.due_at}<span class:overdue={overdue(t)}>{relative(t.due_at)}</span>{/if}
						{#if hasReminder(t)}<span class="bell" title="Reminder set">◔</span>{/if}
						{#if t.priority > 1}<span class="prio">!</span>{/if}
						{#each t.subjects ?? [] as s}<span class="subj">{s}</span>{/each}
					</span>
				{/if}
			</button>
		</div>
	{/each}
</div>

{#if sheet}
	<TaskSheet
		task={sheet}
		onclose={() => (sheet = null)}
		onsaved={() => {
			sheet = null;
			load();
		}}
		ondeleted={async (id) => {
			sheet = null;
			await queueTaskDelete(id);
			tasks = tasks.filter((x) => x.id !== id);
		}}
	/>
{/if}
