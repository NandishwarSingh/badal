/// <reference types="@sveltejs/kit" />

// badal service worker.
//
// The whole UI is precached: every hashed chunk, the fonts, the icons and the
// shell HTML. An app open touches the network only for data. This is the point
// of the thing -- on a phone, opening badal should feel like opening a native
// app, not like loading a website.
//
// API calls are never cached here. IndexedDB is the offline data layer, and a
// stale cached API response would silently contradict it.
//
// The asset list comes from SvelteKit rather than being written by hand: the
// previous version of this file deliberately skipped /_app/immutable because a
// hand-kept list goes stale the moment a chunk is renamed. A generated manifest
// has no such problem.

import { build, files, version } from '$service-worker';

const CACHE = `badal-${version}`;

// '/' is the shell. adapter-static writes it as index.html and Caddy's
// try_files resolves every client-side route to it, so one cached entry
// answers every navigation.
const PRECACHE = [...build, ...files, '/'];

self.addEventListener('install', (e) => {
	// No skipWaiting. A new version installs quietly in the background and only
	// takes over on the next cold start, so a session that is open right now
	// can never have its chunks deleted mid-use.
	e.waitUntil(caches.open(CACHE).then((c) => c.addAll(PRECACHE)));
});

self.addEventListener('activate', (e) => {
	// No clients.claim() either, for the same reason: pages already open keep
	// the worker they booted with.
	e.waitUntil(
		caches
			.keys()
			.then((keys) => Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k))))
	);
});

self.addEventListener('fetch', (e) => {
	const url = new URL(e.request.url);
	if (e.request.method !== 'GET') return;
	if (url.origin !== self.location.origin) return;
	// Let the app own all API traffic.
	if (url.pathname.startsWith('/v1/') || url.pathname === '/health') return;

	// Navigations are answered from cache without waiting on the network. This
	// is the line that removes the stall from every app open; the cost is that
	// a deployed change is picked up on the next cold start rather than this
	// one, which is the trade we want on a phone.
	if (e.request.mode === 'navigate') {
		e.respondWith(caches.match('/').then((hit) => hit || fetch(e.request)));
		return;
	}

	e.respondWith(
		caches.match(e.request).then(
			(hit) =>
				hit ||
				fetch(e.request).then((res) => {
					if (res.ok && res.type === 'basic') {
						const copy = res.clone();
						caches.open(CACHE).then((c) => c.put(e.request, copy));
					}
					return res;
				})
		)
	);
});

// Web Push. Works on Android, and on iOS 16.4+ once installed to the home
// screen -- which is why Settings nags about installing.
self.addEventListener('push', (e) => {
	let d = { title: 'badal', body: '' };
	try { d = e.data ? e.data.json() : d; } catch { d.body = e.data ? e.data.text() : ''; }
	e.waitUntil(
		self.registration.showNotification(d.title || 'badal', {
			body: d.body || '',
			icon: '/icon-192.png',
			badge: '/icon-192.png',
			tag: d.tag || undefined,
			// A reminder that cannot be answered from the lock screen has not
			// really saved you anything.
			actions: Array.isArray(d.actions) ? d.actions.slice(0, 2) : undefined,
			data: { url: d.url || '/', ...(d.data || {}) }
		})
	);
});

self.addEventListener('notificationclick', (e) => {
	e.notification.close();
	const data = e.notification.data || {};
	const target = data.url || '/';

	// Acting on a task from the notification: do it over the network from the
	// worker and never open a window. Opening the app to tick something off is
	// the thing the buttons exist to avoid.
	if (e.action && data.task_id) {
		e.waitUntil(actOnTask(data.task_id, e.action));
		return;
	}
	e.waitUntil(
		self.clients.matchAll({ type: 'window', includeUncontrolled: true }).then((cs) => {
			for (const c of cs) if ('focus' in c) return c.navigate(target).then(() => c.focus());
			return self.clients.openWindow(target);
		})
	);
});

async function actOnTask(id, action) {
	const path =
		action === 'done'
			? `/v1/tasks/${id}/state?state=done`
			: `/v1/tasks/${id}/snooze?minutes=10`;
	try {
		// Cookie only: the worker has no access to the page's device token, so
		// this works for a passkey session. It fails quietly otherwise rather
		// than popping a window the person did not ask for.
		// self.location.origin, not the page's configured base: a worker cannot
		// read localStorage. Caddy serves the app and /v1 from one origin, so
		// these agree in every deployed setup.
		const res = await fetch(self.location.origin + path, {
			method: 'POST',
			credentials: 'include'
		});
		if (!res.ok) throw new Error(String(res.status));
		const open = await self.clients.matchAll({ type: 'window', includeUncontrolled: true });
		for (const c of open) c.postMessage({ type: 'task-changed', id });
	} catch {
		// Could not act -- fall back to opening the task so it can be done by
		// hand, which is better than the button silently doing nothing.
		await self.clients.openWindow(`/?task=${id}`);
	}
}
