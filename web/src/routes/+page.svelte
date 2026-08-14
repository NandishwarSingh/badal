<script>
	import Skeleton from '$lib/Skeleton.svelte';
	// Now: what needs you today.
	//
	// The home surface from docs/DESIGN.md. Tasks are its content, but the point
	// is the question it answers -- "why did I open this?" -- so everything here
	// is actionable in place. Ticking a task must not navigate anywhere.
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { Tasks, Notifications } from '$lib/api.js';
	import { taskCache } from '$lib/store.js';
	import { state as sync, queueTask, queueTaskDelete } from '$lib/sync.svelte.js';
	import { toast } from '$lib/toast.svelte.js';
	import TaskSheet from '$lib/TaskSheet.svelte';
	import Icon from '$lib/Icon.svelte';
	import ActivitySheet from '$lib/ActivitySheet.svelte';

	let view = $state('open'); // open | today | done
	let tasks = $state([]);
	let counts = $state({ open: 0, overdue: 0, today: 0, done: 0 });
	let loading = $state(true);
	let quick = $state('');
	let sheet = $state(null);
	let unread = $state(0);
	let activity = $state(null);

	async function refreshUnread() {
		try {
			unread = (await Notifications.unread()).unread || 0;
		} catch {
			// Offline: leave the last known count rather than showing zero,
			// which would read as "nothing happened".
		}
	}

	async function openActivity() {
		try {
			activity = (await Notifications.list()).notifications || [];
		} catch {
			activity = [];
		}
	}

	async function load() {
		// Cache first, so the list is on screen before the network answers.
		const local = await taskCache.all();
		if (local.length) tasks = sortLocal(local.filter(matchesView));
		try {
			const res = await Tasks.list(view);
			tasks = res.tasks || [];
			counts = res.counts || counts;
			await taskCache.replaceAll(res.tasks || []);
		} catch (e) {
			if (e?.status === 401) {
				goto('/settings');
				return;
			}
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
		// authState is an async function, so `authState.session` was always
		// undefined and this collapsed to "is there a device token in
		// localStorage" -- which bounced every passkey session straight to
		// Settings. The server's answer is the only authoritative one, so
		// load() decides: a 401 redirects, anything else does not.
		load();
		refreshUnread();
		// Poll rather than hold a socket open: one indexed count every couple
		// of minutes costs nothing, and a reminder that fires while the app is
		// open should not wait for a manual refresh.
		const tick = setInterval(refreshUnread, 120000);
		const onVisible = () => {
			if (document.visibilityState === 'visible') {
				refreshUnread();
				load();
			}
		};
		document.addEventListener('visibilitychange', onVisible);
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
		return () => {
			clearInterval(tick);
			document.removeEventListener('visibilitychange', onVisible);
			navigator.serviceWorker?.removeEventListener('message', onMsg);
		};
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

<div class="pagehead">
	<h1>
		Now
		<span class="sub">
			{#if counts.overdue}{counts.overdue} overdue{:else if counts.open}{counts.open} open{:else}All clear{/if}
			{#if sync.syncing}· syncing{/if}
		</span>
	</h1>
	<span class="spacer"></span>
	<!-- Roadmaps: subject checklists built from uploaded PDFs. -->
	<button class="iconbtn" onclick={() => goto('/roadmaps')} aria-label="Roadmaps">
		<svg viewBox="0 0 24 24"><path d="M5 5h14 M5 12h14 M5 19h14 M2.5 5h.01 M2.5 12h.01 M2.5 19h.01" /></svg>
	</button>
	<!-- Timetable: the recurring week lives on its own page, reached here. -->
	<button class="iconbtn" onclick={() => goto('/timetable')} aria-label="Timetable">
		<svg viewBox="0 0 24 24"><path d="M12 4a8 8 0 1 0 0 16 8 8 0 0 0 0-16z M12 8v4.4l3 1.8" /></svg>
	</button>
	<!-- Activity: the in-app record of what fired. A push can be dismissed or
	     never arrive at all, so the count is the thing that is actually true. -->
	<button class="iconbtn bell" onclick={openActivity} aria-label="Activity">
		<svg viewBox="0 0 24 24">
			<path d="M18 8.5a6 6 0 1 0-12 0c0 5-2 6.5-2 6.5h16s-2-1.5-2-6.5" />
			<path d="M10.5 19a2 2 0 0 0 3 0" />
		</svg>
		{#if unread > 0}<span class="belldot">{unread > 9 ? '9+' : unread}</span>{/if}
	</button>
	<!-- Settings lives here, not in the tab bar: monthly use should not cost a
	     fifth of the bar. See docs/DESIGN.md. -->
	<button class="iconbtn" onclick={() => goto('/settings')} aria-label="Settings">
		<svg viewBox="0 0 24 24">
			<path d="M12 12a4 4 0 1 0 0-8 4 4 0 0 0 0 8z M4.5 20a7.5 7.5 0 0 1 15 0" />
		</svg>
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
	<Skeleton rows={4} kind="plain" />
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

{#if tasks.length}
	<div class="group">
		{#each tasks as t (t.id)}
			<div class="grow noicon taskrow" class:isdone={t.state !== 'open'}>
				<button class="taskmain" onclick={() => (sheet = t)}>
					<span class="gtitle">{t.title}</span>
					{#if t.due_at || hasReminder(t) || t.priority > 1 || t.recur_every || t.subjects?.length}
						<span class="gsub">
							{#if t.due_at}<span class:overdue={overdue(t)}>{relative(t.due_at)}</span>{/if}
							{#if hasReminder(t)}<span class="bellmark" title="Reminder set"><Icon name="bell" size={13} label="Reminder set" /></span>{/if}
							{#if t.recur_every}<span class="rep" title="Repeats {t.recur_every}ly"><Icon name="repeat" size={13} label="Repeats" /></span>{/if}
							{#if t.priority > 1}<span class="prio">!</span>{/if}
							{#each t.subjects ?? [] as sj}<span class="subj">{sj}</span>{/each}
						</span>
					{/if}
				</button>

				<!-- On the right, where the thumb already is, and the whole
				     trailing edge is the target. -->
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
			</div>
		{/each}
	</div>
{/if}

{#if activity}
	<ActivitySheet
		items={activity}
		onclose={() => (activity = null)}
		onchanged={refreshUnread}
	/>
{/if}

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

