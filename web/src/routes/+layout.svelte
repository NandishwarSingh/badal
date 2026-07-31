<script>
	import '../app.css';
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { isSignedIn } from '$lib/api.js';
	import { authState } from '$lib/auth.js';
	import { requestPersistence } from '$lib/store.js';
	import { state as sync, startAutoSync, run } from '$lib/sync.svelte.js';
	import { toasts, dismiss } from '$lib/toast.svelte.js';

	let { children } = $props();
	let ready = $state(false);
	let authed = $state(false);

	const tabs = [
		// Five tabs, and they stop growing. Settings moved to the top bar so
		// Now could have a slot without a sixth: see docs/DESIGN.md.
		{ href: '/', label: 'Now', d: 'M12 4a8 8 0 1 0 0 16 8 8 0 0 0 0-16z M12 8v4.4l3 1.8' },
		{ href: '/notes', label: 'Notes', d: 'M5 4h14v16H5z M9 9h6 M9 13h4' },
		{ href: '/new', label: 'Capture', d: 'M12 5v14 M5 12h14' },
		{ href: '/calendar', label: 'Calendar', d: 'M4 6h16v14H4z M4 10h16 M8 3v4 M16 3v4' },
		{ href: '/search', label: 'Search', d: 'M11 4a7 7 0 1 0 0 14 7 7 0 0 0 0-14z M20 20l-4.2-4.2' }
	];



	onMount(async () => {
		try {
			const s = await authState();
			authed = s.signed_in || isSignedIn();
		} catch {
			authed = isSignedIn();
		}
		ready = true;
		if (!authed) return;
		await requestPersistence();
		startAutoSync();
		if ('serviceWorker' in navigator) navigator.serviceWorker.register('/sw.js').catch(() => {});
	});

	const active = (h) => (h === '/' ? page.url.pathname === '/' : page.url.pathname.startsWith(h));
</script>

<div class="app">
	{#if ready && authed}
		<nav class="tabbar">
			{#each tabs as t}
				<a href={t.href} class:active={active(t.href)} aria-label={t.label}>
					<svg viewBox="0 0 24 24"><path d={t.d} /></svg>
					<span>{t.label}</span>
				</a>
			{/each}
		</nav>
	{/if}

	<main>
		{#if ready && authed && !sync.online}
			<div class="banner warn">
				Offline{sync.pending ? ` — ${sync.pending} change${sync.pending > 1 ? 's' : ''} waiting` : ''}.
				Edits save here and upload when you reconnect.
			</div>
		{:else if sync.lastError}
			<div class="banner err">
				{sync.lastError}
				<button class="ghost" onclick={() => run()} style="min-height:0;padding:2px 8px">Retry</button>
			</div>
		{/if}
		{@render children?.()}
	</main>

	{#each toasts.items as t (t.id)}
		<div class="toast" class:actionable={t.actionLabel}>
			<span>{t.text}</span>
			{#if t.actionLabel}
				<button class="toast-action" onclick={() => { t.onAction(); dismiss(t.id); }}>
					{t.actionLabel}
				</button>
			{/if}
		</div>
	{/each}
</div>
