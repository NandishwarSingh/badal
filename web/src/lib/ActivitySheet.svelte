<script>
	import { Notifications } from '$lib/api.js';
	import { goto } from '$app/navigation';

	let { items = [], onclose, onchanged } = $props();

	const EXIT_MS = 220;
	let closing = $state(false);
	let list = $state(items);

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

	// Opening the list is the act of reading it, so the badge clears here
	// rather than making you dismiss each row.
	$effect(() => {
		if (list.some((n) => !n.read_at)) {
			Notifications.read().then(() => onchanged?.()).catch(() => {});
		}
	});

	async function open(n) {
		await Notifications.read(n.id).catch(() => {});
		closing = true;
		setTimeout(() => {
			onclose?.();
			goto(n.url || '/');
		}, EXIT_MS);
	}

	async function clear() {
		await Notifications.clear().catch(() => {});
		list = list.filter((n) => !n.read_at);
		onchanged?.();
	}

	const icon = { reminder: '◔', review: '◇', digest: '☀', job: '⚙', system: '!' };

	function when(iso) {
		const d = new Date(iso);
		const mins = Math.round((Date.now() - d) / 60000);
		if (mins < 1) return 'just now';
		if (mins < 60) return `${mins}m ago`;
		if (mins < 1440) return `${Math.round(mins / 60)}h ago`;
		return d.toLocaleDateString(undefined, { day: 'numeric', month: 'short' });
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
	<div class="row" style="align-items:baseline">
		<h2>Activity</h2>
		<span class="spacer"></span>
		{#if list.some((n) => n.read_at)}
			<button class="ghost" style="padding:4px 10px;min-height:0" onclick={clear}>Clear read</button>
		{/if}
	</div>

	{#if list.length === 0}
		<div class="empty">
			<p>Nothing yet.</p>
			<span class="muted">Reminders and things needing review land here, even if the notification never arrives.</span>
		</div>
	{/if}

	<div class="actlist">
		{#each list as n (n.id)}
			<button class="actrow" class:unseen={!n.read_at} onclick={() => open(n)}>
				<span class="acticon">{icon[n.kind] ?? '•'}</span>
				<span class="actbody">
					<span class="acttitle">{n.title}</span>
					{#if n.body}<span class="actsub">{n.body}</span>{/if}
					<span class="actwhen">{when(n.created_at)}</span>
				</span>
			</button>
		{/each}
	</div>

	<div class="sheet-actions">
		<button class="sheet-close" onclick={close}>Close</button>
	</div>
</div>
