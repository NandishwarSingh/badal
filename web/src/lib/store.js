// Offline store: an IndexedDB cache of notes plus an outbox of pending writes.
//
// iOS PWAs cannot sync in the background, so everything reconciles on app open
// and on reconnect. The server stays the source of truth -- if Safari evicts
// this database, the cost is a re-sync, not data loss.

const DB_NAME = 'badal';
const DB_VERSION = 1;

let dbp = null;

function open() {
	if (dbp) return dbp;
	dbp = new Promise((resolve, reject) => {
		const req = indexedDB.open(DB_NAME, DB_VERSION);
		req.onupgradeneeded = () => {
			const db = req.result;
			if (!db.objectStoreNames.contains('notes')) {
				const s = db.createObjectStore('notes', { keyPath: 'slug' });
				s.createIndex('updated_at', 'updated_at');
			}
			// Queued writes, replayed in insertion order so a rename followed
			// by an edit cannot land backwards.
			if (!db.objectStoreNames.contains('outbox')) {
				db.createObjectStore('outbox', { keyPath: 'id', autoIncrement: true });
			}
			if (!db.objectStoreNames.contains('meta')) {
				db.createObjectStore('meta', { keyPath: 'k' });
			}
		};
		req.onsuccess = () => resolve(req.result);
		req.onerror = () => reject(req.error);
	});
	return dbp;
}

function tx(store, mode, fn) {
	return open().then(
		(db) =>
			new Promise((resolve, reject) => {
				const t = db.transaction(store, mode);
				const s = t.objectStore(store);
				let out;
				try {
					out = fn(s);
				} catch (e) {
					reject(e);
					return;
				}
				t.oncomplete = () => resolve(out && out.result !== undefined ? out.result : out);
				t.onerror = () => reject(t.error);
			})
	);
}

const all = (store) =>
	open().then(
		(db) =>
			new Promise((resolve, reject) => {
				const req = db.transaction(store).objectStore(store).getAll();
				req.onsuccess = () => resolve(req.result || []);
				req.onerror = () => reject(req.error);
			})
	);

// ------------------------------------------------------------------ notes

export const cache = {
	all: () => all('notes'),
	get: (slug) =>
		open().then(
			(db) =>
				new Promise((resolve, reject) => {
					const r = db.transaction('notes').objectStore('notes').get(slug);
					r.onsuccess = () => resolve(r.result || null);
					r.onerror = () => reject(r.error);
				})
		),
	put: (note) => tx('notes', 'readwrite', (s) => s.put(note)),
	del: (slug) => tx('notes', 'readwrite', (s) => s.delete(slug)),
	putMany: (notes) =>
		tx('notes', 'readwrite', (s) => {
			notes.forEach((n) => s.put(n));
		}),
	clear: () => tx('notes', 'readwrite', (s) => s.clear())
};

// ----------------------------------------------------------------- outbox

export const outbox = {
	all: () => all('outbox'),
	add: (op) => tx('outbox', 'readwrite', (s) => s.add({ ...op, queued_at: Date.now() })),
	remove: (id) => tx('outbox', 'readwrite', (s) => s.delete(id)),
	count: () => all('outbox').then((r) => r.length)
};

// ------------------------------------------------------------------- meta

export const meta = {
	get: (k, def = null) =>
		open().then(
			(db) =>
				new Promise((resolve) => {
					const r = db.transaction('meta').objectStore('meta').get(k);
					r.onsuccess = () => resolve(r.result ? r.result.v : def);
					r.onerror = () => resolve(def);
				})
		),
	set: (k, v) => tx('meta', 'readwrite', (s) => s.put({ k, v }))
};

// Ask Safari not to evict us. Home-screen installs are normally granted.
export async function requestPersistence() {
	if (navigator.storage?.persist) {
		try {
			return await navigator.storage.persist();
		} catch {
			return false;
		}
	}
	return false;
}
