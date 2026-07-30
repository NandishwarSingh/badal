<script>
	import { onMount } from 'svelte';
	import { getBase, getToken, setCreds, clearCreds, api } from '$lib/api.js';
	import { cache, outbox, requestPersistence } from '$lib/store.js';
	import { state as sync, run, startAutoSync } from '$lib/sync.svelte.js';

	let base = $state('');
	let token = $state('');
	let signedIn = $state(false);
	let err = $state('');
	let busy = $state(false);
	let device = $state(null);
	let persisted = $state(false);
	let counts = $state({ notes: 0, queued: 0 });
	let installEvent = $state(null);
	let standalone = $state(false);

	onMount(async () => {
		base = getBase();
		signedIn = !!getToken();
		standalone =
			window.matchMedia('(display-mode: standalone)').matches || window.navigator.standalone === true;
		if (navigator.storage?.persisted) persisted = await navigator.storage.persisted();
		window.addEventListener('beforeinstallprompt', (e) => {
			e.preventDefault();
			installEvent = e;
		});
		if (signedIn) await refresh();
	});

	async function refresh() {
		counts = { notes: (await cache.all()).length, queued: await outbox.count() };
		try {
			const d = await api('/v1/devices');
			device = (d.devices || []).find((x) => !x.revoked_at) || null;
		} catch { /* offline */ }
	}

	async function signIn() {
		err = '';
		busy = true;
		try {
			setCreds(base, token.trim());
			await api('/health'); // cheap check that the origin is right
			await api('/v1/notes'); // and that the token is accepted
			signedIn = true;
			token = '';
			await requestPersistence();
			startAutoSync();
			await refresh();
		} catch (e) {
			clearCreds();
			err = e.status === 401 ? 'That token was rejected.' : e.message || 'Could not connect.';
		} finally {
			busy = false;
		}
	}

	async function signOut() {
		if (!confirm('Sign out and clear local notes on this device?')) return;
		clearCreds();
		await cache.clear();
		location.href = '/';
	}

	async function install() {
		if (!installEvent) return;
		installEvent.prompt();
		installEvent = null;
	}
</script>

<div class="topbar"><h1>Settings</h1></div>

{#if !signedIn}
	<p class="muted" style="margin:0 2px 14px">
		Register a device on your Mac with <code>badal</code>, then paste its token here.
	</p>
	<label class="muted" for="b">Server</label>
	<input id="b" bind:value={base} placeholder="https://badal.nandishwarsingh.com" style="margin:6px 0 12px" />
	<label class="muted" for="t">Device token</label>
	<input id="t" bind:value={token} placeholder="bdl_…" type="password" style="margin:6px 0 14px" />
	{#if err}<div class="banner err">{err}</div>{/if}
	<button class="primary" onclick={signIn} disabled={busy || !token.trim()} style="width:100%">
		{busy ? 'Checking…' : 'Sign in'}
	</button>
{:else}
	{#if !standalone}
		<div class="card">
			<h3>Install badal</h3>
			<p style="-webkit-line-clamp:4;line-clamp:4">
				Add to your home screen for offline access and push notifications. On iPhone: Share → Add
				to Home Screen. Notifications only work once installed.
			</p>
			{#if installEvent}
				<button class="primary" onclick={install} style="margin-top:10px">Install</button>
			{/if}
		</div>
	{/if}

	<div class="card">
		<h3>This device</h3>
		<div class="row" style="margin-top:6px">
			<span class="muted">{device ? device.name : 'unknown'}</span>
			<span class="spacer"></span>
			<span class="pill {sync.online ? 'ok' : 'warn'}">{sync.online ? 'online' : 'offline'}</span>
		</div>
		<div class="row" style="margin-top:8px">
			<span class="muted">{counts.notes} notes cached · {counts.queued} queued</span>
		</div>
		<div class="row" style="margin-top:8px">
			<span class="pill {persisted ? 'ok' : 'warn'}">
				{persisted ? 'storage persisted' : 'storage may be evicted'}
			</span>
		</div>
	</div>

	<div class="row" style="margin-top:14px">
		<button onclick={() => run().then(refresh)}>Sync now</button>
		<span class="spacer"></span>
		<button class="danger" onclick={signOut}>Sign out</button>
	</div>

	<p class="muted" style="margin-top:18px;font-size:12px">
		Server: {base}
	</p>
{/if}
