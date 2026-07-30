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
			data: { url: d.url || '/' }
		})
	);
});

self.addEventListener('notificationclick', (e) => {
	e.notification.close();
	const target = e.notification.data?.url || '/';
	e.waitUntil(
		self.clients.matchAll({ type: 'window', includeUncontrolled: true }).then((cs) => {
			for (const c of cs) if ('focus' in c) return c.navigate(target).then(() => c.focus());
			return self.clients.openWindow(target);
		})
	);
});
