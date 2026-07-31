// badal service worker.
//
// App shell is cache-first so the UI opens instantly and works with no
// network. API calls are never cached here -- IndexedDB is the offline data
// layer, and a stale cached API response would silently contradict it.

const CACHE = 'badal-shell-__BUILD__';
// Deliberately excludes hashed /_app/immutable assets: a deploy deletes the
// old ones, and pre-caching them means a stale shell references files that
// no longer exist.
const SHELL = ['/', '/manifest.webmanifest', '/icon-192.png'];

self.addEventListener('install', (e) => {
	e.waitUntil(caches.open(CACHE).then((c) => c.addAll(SHELL)).then(() => self.skipWaiting()));
});

self.addEventListener('activate', (e) => {
	e.waitUntil(
		caches.keys()
			.then((keys) => Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k))))
			.then(() => self.clients.claim())
	);
});

self.addEventListener('fetch', (e) => {
	const url = new URL(e.request.url);
	if (e.request.method !== 'GET') return;
	if (url.origin !== self.location.origin) return;
	// Let the app own all API traffic.
	if (url.pathname.startsWith('/v1/') || url.pathname === '/health') return;

	// Navigations: network first, fall back to the cached shell when offline.
	if (e.request.mode === 'navigate') {
		e.respondWith(
			fetch(e.request).catch(() => caches.match('/index.html').then((r) => r || caches.match('/')))
		);
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
