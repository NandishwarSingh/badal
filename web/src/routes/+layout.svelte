<script>
	import '../app.css';
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { isSignedIn } from '$lib/api.js';
	import { authState } from '$lib/auth.js';
	import { requestPersistence } from '$lib/store.js';
	import { state as sync, startAutoSync, run } from '$lib/sync.svelte.js';
	import { toasts } from '$lib/toast.svelte.js';

	let { children } = $props();
	let ready = $state(false);
	let authed = $state(false);

	onMount(async () => {
		// Either credential is fine: a passkey session cookie, or a device
		// token pasted into Settings.
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

	const tabs = [
		{ href: '/', label: 'Notes', d: 'M5 4h14v16H5z M9 9h6 M9 13h4' },
		{ href: '/search', label: 'Search', d: 'M11 4a7 7 0 1 0 0 14 7 7 0 0 0 0-14z M20 20l-4.2-4.2' },
		{ href: '/new', label: 'Capture', d: 'M12 5v14 M5 12h14' },
		{ href: '/settings', label: 'You', d: 'M12 12a4 4 0 1 0 0-8 4 4 0 0 0 0 8z M4.5 20a7.5 7.5 0 0 1 15 0' }
	];

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
		<div class="toast">{t.text}</div>
	{/each}
</div>
