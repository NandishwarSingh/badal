// Web Push subscription. On iOS this only works once the PWA is installed to
// the home screen -- Safari silently refuses otherwise, which is why the UI
// checks display-mode before offering it.

const urlB64ToUint8 = (s) => {
	const pad = '='.repeat((4 - (s.length % 4)) % 4);
	const raw = atob((s + pad).replace(/-/g, '+').replace(/_/g, '/'));
	return Uint8Array.from(raw, (c) => c.charCodeAt(0));
};

export function pushSupported() {
	return typeof window !== 'undefined' && 'serviceWorker' in navigator && 'PushManager' in window;
}

export function isInstalled() {
	if (typeof window === 'undefined') return false;
	return window.matchMedia('(display-mode: standalone)').matches || window.navigator.standalone === true;
}

export async function pushState() {
	if (!pushSupported()) return { supported: false, subscribed: false, permission: 'unsupported' };
	const reg = await navigator.serviceWorker.ready;
	const sub = await reg.pushManager.getSubscription();
	return { supported: true, subscribed: !!sub, permission: Notification.permission };
}

export async function subscribePush() {
	if (!pushSupported()) throw new Error('Push is not supported in this browser.');
	if (!isInstalled() && /iPhone|iPad|iPod/.test(navigator.userAgent)) {
		throw new Error('On iPhone, add badal to your Home Screen first — Safari only allows push for installed apps.');
	}
	const perm = await Notification.requestPermission();
	if (perm !== 'granted') throw new Error('Notification permission was denied.');

	const { public_key } = await (await fetch('/v1/push/key', { credentials: 'include' })).json();
	const reg = await navigator.serviceWorker.ready;
	let sub = await reg.pushManager.getSubscription();
	if (!sub) {
		sub = await reg.pushManager.subscribe({
			userVisibleOnly: true,
			applicationServerKey: urlB64ToUint8(public_key)
		});
	}
	const res = await fetch('/v1/push/subscribe', {
		method: 'POST',
		credentials: 'include',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify(sub.toJSON())
	});
	if (!res.ok) throw new Error('Server rejected the subscription.');
	return true;
}

export async function unsubscribePush() {
	const reg = await navigator.serviceWorker.ready;
	const sub = await reg.pushManager.getSubscription();
	if (!sub) return;
	await fetch('/v1/push/unsubscribe', {
		method: 'POST',
		credentials: 'include',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ endpoint: sub.endpoint })
	}).catch(() => {});
	await sub.unsubscribe();
}

export async function sendTestPush() {
	const res = await fetch('/v1/push/test', { method: 'POST', credentials: 'include' });
	if (!res.ok) throw new Error('Test failed.');
	return res.json();
}
