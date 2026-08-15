<script>
	import Icon from '$lib/Icon.svelte';
	import { onMount } from 'svelte';
	import { getBase, getToken, setCreds, clearCreds, rememberSignedIn, api, echo } from '$lib/api.js';
	import { cache, outbox, requestPersistence } from '$lib/store.js';
	import { state as sync, run, startAutoSync } from '$lib/sync.svelte.js';
	import {
		authState, registerPasskey, loginWithPasskey, logout,
		listCredentials, platformAuthenticatorAvailable
	} from '$lib/auth.js';
	import { pushState, subscribePush, unsubscribePush, sendTestPush, isInstalled } from '$lib/push.js';
	import { toast } from '$lib/toast.svelte.js';
	import { Settings, Mail, Jobs } from '$lib/api.js';

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
	let cfg = $state(null);
	let mailAccts = $state([]);
	let jobs = $state(null);

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
			cfg = await Settings.get().catch(() => null);
			mailAccts = await Mail.accounts().then((r) => r.accounts || []).catch(() => []);
			jobs = await Jobs.stats().catch(() => null);
		}
	}

	async function doLogin() {
		err = ''; busy = 'login';
		try {
			await loginWithPasskey();
			rememberSignedIn(true);
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
			rememberSignedIn(true);
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
			rememberSignedIn(true);
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

	function ago(iso) {
		const m = (Date.now() - new Date(iso)) / 60000;
		if (m < 1) return 'just now';
		if (m < 60) return `${Math.floor(m)}m ago`;
		if (m < 1440) return `${Math.floor(m / 60)}h ago`;
		return `${Math.floor(m / 1440)}d ago`;
	}

	async function setCfg(key, value) {
		const prev = cfg?.[key];
		cfg = { ...cfg, [key]: value }; // optimistic
		try {
			cfg = await Settings.set({ [key]: value });
			toast('Saved');
		} catch (e) {
			cfg = { ...cfg, [key]: prev };
			err = e.message;
		}
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

<div class="pagehead">
	<h1>
		You
		<span class="sub">{signedIn ? 'Signed in' : 'Not signed in'}</span>
	</h1>
	<span class="spacer"></span>
	<button class="iconbtn" onclick={() => run().then(refresh)} aria-label="Sync now" title="Sync now">
		<svg viewBox="0 0 24 24"><path d="M20 11a8 8 0 1 0-.6 4 M20 5v6h-6" /></svg>
	</button>
</div>

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
		<div class="callout">
			<div class="badge" style="background:var(--warn)">
				<svg viewBox="0 0 24 24" width="21" height="21" fill="none" stroke="currentColor" stroke-width="2.1"
					stroke-linecap="round" stroke-linejoin="round">
					<path d="M12 16V4 M8 8l4-4 4 4 M4 16v3a1 1 0 0 0 1 1h14a1 1 0 0 0 1-1v-3" />
				</svg>
			</div>
			<p class="lead">Add badal to your Home Screen.</p>
			<p>
				Share → Add to Home Screen. On iPhone this is required before notifications work at all,
				and it stops Safari evicting your offline notes.
			</p>
		</div>
	{/if}

	<div class="groupcols">
		<div>
			<div class="sectionhead"><h2>Alerts</h2></div>
			<div class="group">
				<div class="grow">
					<span class="gtile red">
						<svg viewBox="0 0 24 24"><path d="M18 8.5a6 6 0 1 0-12 0c0 5-2 6.5-2 6.5h16s-2-1.5-2-6.5" /><path d="M10.5 19a2 2 0 0 0 3 0" /></svg>
					</span>
					<span class="gbody">
						<span class="gtitle">Notifications</span>
						<span class="gsub">Web Push — no Apple account needed</span>
					</span>
					<span class="pill {push.subscribed ? 'ok' : ''}">{push.subscribed ? 'on' : 'off'}</span>
				</div>
				<div class="grow noicon" style="gap:8px">
					<button onclick={togglePush} disabled={busy === 'push' || !push.supported}>
						{#if busy === 'push'}<span class="spinner"></span>{/if}
						{push.subscribed ? 'Turn off' : 'Turn on'}
					</button>
					{#if push.subscribed}
						<button class="ghost" onclick={testPush} disabled={busy === 'test'}>Send test</button>
					{/if}
				</div>
			</div>
		</div>

		<div>
			<div class="sectionhead">
				<h2>Sign-in</h2>
				<span class="count">{creds.length} passkey{creds.length === 1 ? '' : 's'}</span>
			</div>
			<div class="group">
				{#each creds as c}
					<div class="grow">
						<span class="gtile purple">
							<svg viewBox="0 0 24 24"><path d="M12 12a4 4 0 1 0 0-8 4 4 0 0 0 0 8z M4.5 20a7.5 7.5 0 0 1 15 0" /></svg>
						</span>
						<span class="gbody">
							<span class="gtitle">{c.label}</span>
							{#if c.synced}<span class="gsub">Synced across your devices</span>{/if}
						</span>
					</div>
				{/each}
				{#if canPasskey}
					<button class="grow" onclick={doEnrol} disabled={busy === 'enrol'}>
						<span class="gtile slate">
							<svg viewBox="0 0 24 24"><path d="M12 5v14 M5 12h14" /></svg>
						</span>
						<span class="gbody"><span class="gtitle">Add a passkey for this device</span></span>
						{#if busy === 'enrol'}<span class="spinner"></span>{:else}<span class="chev"><Icon name="chevron" size={15} stroke={2} /></span>{/if}
					</button>
				{/if}
			</div>
		</div>

		<div>
			<div class="sectionhead"><h2>Storage</h2></div>
			<div class="group">
				<div class="grow">
					<span class="gtile blue">
						<svg viewBox="0 0 24 24"><path d="M4 7c0-1.7 3.6-3 8-3s8 1.3 8 3-3.6 3-8 3-8-1.3-8-3z M4 7v10c0 1.7 3.6 3 8 3s8-1.3 8-3V7" /></svg>
					</span>
					<span class="gbody">
						<span class="gtitle">{counts.notes} notes cached</span>
						<span class="gsub">{counts.queued} waiting to sync</span>
					</span>
					<span class="pill {sync.online ? 'ok' : 'warn'}">{sync.online ? 'online' : 'offline'}</span>
				</div>
				<div class="grow">
					<span class="gtile {persisted ? 'green' : 'orange'}">
						<svg viewBox="0 0 24 24"><path d="M12 3l7 3v6c0 4.2-2.9 7.6-7 9-4.1-1.4-7-4.8-7-9V6z" /></svg>
					</span>
					<span class="gbody">
						<span class="gtitle">{persisted ? 'Storage persisted' : 'Eviction possible'}</span>
						<span class="gsub">
							{persisted
								? 'The browser will not clear your offline copy'
								: 'Install to the Home Screen to make this durable'}
						</span>
					</span>
				</div>
			</div>
		</div>
	</div>

	{#if err}<div class="banner err" style="margin-top:14px">{err}</div>{/if}

	<div class="row" style="margin-top:18px">
		<button onclick={() => run().then(refresh)}>Sync now</button>
		<span class="spacer"></span>
		<button class="danger" onclick={doLogout}>Sign out</button>
	</div>
{/if}
