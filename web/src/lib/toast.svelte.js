// Tiny toast queue. Optimistic writes need visible confirmation, or the UI
// just looks like nothing happened.
export const toasts = $state({ items: [] });
let seq = 0;

export function toast(text, ms = 1900) {
	const id = ++seq;
	toasts.items = [...toasts.items, { id, text }];
	setTimeout(() => {
		toasts.items = toasts.items.filter((t) => t.id !== id);
	}, ms);
}
