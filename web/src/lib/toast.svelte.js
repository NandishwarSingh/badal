// Toast queue. Supports an optional action, so a destructive operation can
// offer undo instead of a confirm dialog.
export const toasts = $state({ items: [] });
let seq = 0;

export function toast(text, ms = 1900) {
	const id = ++seq;
	toasts.items = [...toasts.items, { id, text }];
	setTimeout(() => dismiss(id), ms);
	return id;
}

export function toastAction(text, actionLabel, onAction, ms = 5000) {
	const id = ++seq;
	toasts.items = [...toasts.items, { id, text, actionLabel, onAction }];
	setTimeout(() => dismiss(id), ms);
	return id;
}

export function dismiss(id) {
	toasts.items = toasts.items.filter((t) => t.id !== id);
}
