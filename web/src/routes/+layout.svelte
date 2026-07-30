<script>
	import '../app.css';
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { isSignedIn } from '$lib/api.js';
	import { requestPersistence } from '$lib/store.js';
	import { state as sync, startAutoSync, run } from '$lib/sync.svelte.js';

	let { children } = $props();
	let signedIn = $state(false);

	onMount(async () => {
		signedIn = isSignedIn();
		if (!signedIn) return;
		await requestPersistence();
		startAutoSync();
		if ('serviceWorker' in navigator) {
			navigator.serviceWorker.register('/sw.js').catch(() => {});
		}
	});

	const tabs = [
		{ href: '/', label: 'Notes', d: 'M4 4h16v16H4z M8 9h8 M8 13h6' },
		{ href: '/search', label: 'Search', d: 'M11 4a7 7 0 1 0 0 14 7 7 0 0 0 0-14z M20 20l-4-4' },
		{ href: '/new', label: 'New', d: 'M12 5v14 M5 12h14' },
		{ href: '/settings', label: 'Settings', d: 'M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6z M19 12a7 7 0 0 0-.1-1l2-1.6-2-3.4-2.4 1a7 7 0 0 0-1.7-1L14.5 3h-4l-.3 2.6a7 7 0 0 0-1.7 1l-2.4-1-2 3.4L6 11a7 7 0 0 0 0 2l-2 1.6 2 3.4 2.4-1a7 7 0 0 0 1.7 1l.3 2.6h4l.3-2.6a7 7 0 0 0 1.7-1l2.4 1 2-3.4-2-1.6c.1-.3.1-.7.1-1z' }
	];

	const active = (href) =>
		href === '/' ? page.url.pathname === '/' : page.url.pathname.startsWith(href);
</script>

<div class="app">
	{#if signedIn}
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
		{#if signedIn && !sync.online}
			<div class="banner warn">
				Offline{sync.pending ? ` — ${sync.pending} change${sync.pending > 1 ? 's' : ''} queued` : ''}. Edits
				save locally and upload when you reconnect.
			</div>
		{:else if sync.lastError}
			<div class="banner err">{sync.lastError} <button class="ghost" onclick={() => run()}>Retry</button></div>
		{/if}
		{@render children?.()}
	</main>
</div>
