<script>
	import { onMount } from 'svelte';
	import { getBase, getToken, setCreds, clearCreds, api, echo } from '$lib/api.js';
	import { cache, outbox, requestPersistence } from '$lib/store.js';
	import { state as sync, run, startAutoSync } from '$lib/sync.svelte.js';
	import {
		authState, registerPasskey, loginWithPasskey, logout,
		listCredentials, platformAuthenticatorAvailable
	} from '$lib/auth.js';
	import { pushState, subscribePush, unsubscribePush, sendTestPush, isInstalled } from '$lib/push.js';
	import { toast } from '$lib/toast.svelte.js';

	let auth = $state({ has_passkeys: false, signed_in: false });
	let tokenSignedIn = $state(false);
	let canPasskey = $state(false);
	let installed = $state(false);
	let creds = $state([]);
	let push = $state({ supported: false, subscribed: false, permission: 'default' });
	let counts = $state({ notes: 0, queued: 0 });
	let persisted = $state(false);
	let busy = $state('');
	let err = $state('');

	// fallback token sign-in
	let base = $state('');
	let token = $state('');
	let enrolSecret = $state('');
	let diag = $state(null);

	const signedIn = $derived(auth.signed_in || tokenSignedIn);

	onMount(async () => {
		base = getBase();
		tokenSignedIn = !!getToken();
		installed = isInstalled();
		canPasskey = await platformAuthenticatorAvailable();
		if (navigator.storage?.persisted) persisted = await navigator.storage.persisted();
		await refresh();
	});

	async function refresh() {
		try { auth = await authState(); } catch {}
		counts = { notes: (await cache.all()).length, queued: await outbox.count() };
		if (signedIn) {
			creds = await listCredentials().catch(() => []);
			push = await pushState().catch(() => push);
		}
	}

	async function doLogin() {
		err = ''; busy = 'login';
		try {
			await loginWithPasskey();
			toast('Signed in');
			await requestPersistence(); startAutoSync(); await refresh(); await run();
		} catch (e) { err = e.message || 'Sign-in failed.'; }
		finally { busy = ''; }
	}

	async function doEnrol() {
		err = ''; busy = 'enrol';
		try {
			await registerPasskey(navigator.userAgent.includes('iPhone') ? 'iPhone' : 'This device', enrolSecret.trim());
			enrolSecret = '';
			toast('Passkey created');
			if (!auth.signed_in) await loginWithPasskey();
			await requestPersistence(); startAutoSync(); await refresh(); await run();
		} catch (e) { err = e.message || 'Could not create a passkey.'; }
		finally { busy = ''; }
	}

	async function doLogout() {
		await logout(); clearCreds(); await cache.clear(); location.href = '/';
	}

	async function runDiag() {
		busy = 'diag'; diag = null; err = '';
		try {
			setCreds(base || getBase(), token);
			diag = await echo();
		} catch (e) {
			err = `${e.status ? 'HTTP ' + e.status : 'Network error'} — ${e.message}`;
		} finally { busy = ''; }
	}

	async function tokenSignIn() {
		err = ''; busy = 'token';
		try {
			const clean = setCreds(base, token);
			if (!clean) throw new Error('Token looked empty after cleaning.');
			await api('/v1/notes');
			tokenSignedIn = true; token = '';
			toast('Signed in');
			await requestPersistence(); startAutoSync(); await refresh();
		} catch (e) {
			clearCreds();
			// Say which request failed and why -- there are no devtools on a phone.
			err = `${e.status ? 'HTTP ' + e.status : 'Network error'} from ${e.url || base}` +
				(e.status === 401 ? ' — token not accepted. Check for a missing character when pasting.' :
				 e.status ? ` — ${e.message}` : ' — is the server reachable?');
		} finally { busy = ''; }
	}

	async function togglePush() {
		err = ''; busy = 'push';
		try {
			if (push.subscribed) { await unsubscribePush(); toast('Notifications off'); }
			else { await subscribePush(); toast('Notifications on'); }
			push = await pushState();
		} catch (e) { err = e.message + (e.status ? ` (HTTP ${e.status})` : ''); }
		finally { busy = ''; }
	}

	async function testPush() {
		busy = 'test';
		try {
			const r = await sendTestPush();
			toast(r.sent ? `Sent to ${r.sent} device${r.sent > 1 ? 's' : ''}` : 'No devices subscribed');
		} catch (e) { err = e.message + (e.status ? ` (HTTP ${e.status})` : ''); }
		finally { busy = ''; }
	}
</script>

<div class="topbar"><h1>You</h1></div>

{#if !signedIn}
	<div class="card">
		<h3>Sign in</h3>
		<p style="-webkit-line-clamp:4;line-clamp:4">
			{#if auth.has_passkeys}
				Use Face ID, Touch ID, or your device PIN.
			{:else}
				No passkey exists yet. Create the first one with your enrolment secret.
			{/if}
		</p>

		{#if canPasskey && auth.has_passkeys}
			<button class="primary" style="width:100%;margin-top:12px" onclick={doLogin} disabled={busy === 'login'}>
				{#if busy === 'login'}<span class="spinner"></span>{/if} Sign in with passkey
			</button>
		{/if}

		{#if canPasskey && !auth.has_passkeys}
			<input bind:value={enrolSecret} placeholder="Enrolment secret" type="password" style="margin-top:12px" />
			<button class="primary" style="width:100%;margin-top:9px" onclick={doEnrol}
				disabled={busy === 'enrol' || !enrolSecret.trim()}>
				{#if busy === 'enrol'}<span class="spinner"></span>{/if} Create passkey
			</button>
		{/if}

		{#if !canPasskey}
			<div class="banner warn" style="margin-top:12px">
				This browser has no platform authenticator. Use a device token below.
			</div>
		{/if}
	</div>

	<details style="margin-top:6px">
		<summary class="muted" style="padding:10px 4px;cursor:pointer">Sign in with a device token instead</summary>
		<div style="padding:4px 2px 0">
			<input bind:value={base} placeholder="https://badal.nandishwarsingh.com" style="margin-bottom:9px" />
			<input bind:value={token} placeholder="bdl_…" type="password" style="margin-bottom:11px" />
			<div class="row">
				<button style="flex:1" onclick={tokenSignIn} disabled={busy === 'token' || !token.trim()}>Sign in</button>
				<button class="ghost" onclick={runDiag} disabled={busy === 'diag'}>Diagnose</button>
			</div>
			{#if diag}
				<div class="banner info" style="margin-top:11px;font-family:ui-monospace;font-size:12px">
					sent header: {diag.authorization_header_present ? 'yes' : 'NO'}<br />
					token length: {diag.token_len} (expected 47)<br />
					looks like: {diag.token_prefix}…{diag.token_suffix}<br />
					server accepted: {diag.token_accepted ? 'YES' : 'no'}<br />
					session cookie: {diag.has_session_cookie ? 'yes' : 'no'}
				</div>
			{/if}
		</div>
	</details>

	{#if err}<div class="banner err" style="margin-top:12px">{err}</div>{/if}
{:else}
	{#if !installed}
		<div class="card">
			<h3>Add to Home Screen</h3>
			<p style="-webkit-line-clamp:5;line-clamp:5">
				Share → Add to Home Screen. On iPhone this is required before notifications will work at
				all, and it stops Safari evicting your offline notes.
			</p>
		</div>
	{/if}

	<div class="card">
		<div class="row">
			<h3>Notifications</h3>
			<span class="spacer"></span>
			<span class="pill {push.subscribed ? 'ok' : ''}">{push.subscribed ? 'on' : 'off'}</span>
		</div>
		<p style="-webkit-line-clamp:3;line-clamp:3">
			Web Push — no Apple Developer account needed.
		</p>
		<div class="row" style="margin-top:11px">
			<button onclick={togglePush} disabled={busy === 'push' || !push.supported}>
				{#if busy === 'push'}<span class="spinner"></span>{/if}
				{push.subscribed ? 'Turn off' : 'Turn on'}
			</button>
			{#if push.subscribed}
				<button class="ghost" onclick={testPush} disabled={busy === 'test'}>Send test</button>
			{/if}
		</div>
	</div>

	<div class="card">
		<div class="row">
			<h3>Passkeys</h3>
			<span class="spacer"></span>
			<span class="pill">{creds.length}</span>
		</div>
		{#each creds as c}
			<div class="row" style="margin-top:8px">
				<span class="muted">{c.label}</span>
				<span class="spacer"></span>
				{#if c.synced}<span class="pill ok">synced</span>{/if}
			</div>
		{/each}
		{#if canPasskey}
			<button style="width:100%;margin-top:11px" onclick={doEnrol} disabled={busy === 'enrol'}>
				{#if busy === 'enrol'}<span class="spinner"></span>{/if} Add a passkey for this device
			</button>
		{/if}
	</div>

	<div class="card">
		<h3>Storage</h3>
		<div class="row" style="margin-top:7px">
			<span class="muted">{counts.notes} notes cached · {counts.queued} queued</span>
			<span class="spacer"></span>
			<span class="pill {sync.online ? 'ok' : 'warn'}">{sync.online ? 'online' : 'offline'}</span>
		</div>
		<div class="row" style="margin-top:8px">
			<span class="pill {persisted ? 'ok' : 'warn'}">
				{persisted ? 'storage persisted' : 'eviction possible'}
			</span>
		</div>
	</div>

	{#if err}<div class="banner err">{err}</div>{/if}

	<div class="row" style="margin-top:14px">
		<button onclick={() => run().then(refresh)}>Sync now</button>
		<span class="spacer"></span>
		<button class="danger" onclick={doLogout}>Sign out</button>
	</div>
{/if}
