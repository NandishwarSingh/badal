// API client. Every call goes through here so the offline queue has exactly
// one place to intercept writes.

const TOKEN_KEY = 'badal.token';
const BASE_KEY = 'badal.base';

export function getToken() {
	return localStorage.getItem(TOKEN_KEY) || '';
}
export function getBase() {
	return localStorage.getItem(BASE_KEY) || window.location.origin;
}
export function setCreds(base, token) {
	// iOS paste can carry newlines, non-breaking spaces and zero-width chars.
	// Strip anything that is not part of the token alphabet rather than
	// failing with an unhelpful 401.
	const clean = (token || '').replace(/[^A-Za-z0-9_\-]/g, '');
	localStorage.setItem(BASE_KEY, (base || window.location.origin).trim().replace(/\/+$/, ''));
	localStorage.setItem(TOKEN_KEY, clean);
	return clean;
}

/** What the server actually sees. Used by the in-app diagnostic. */
export function echo() {
	return api('/v1/auth/echo');
}
export function clearCreds() {
	localStorage.removeItem(TOKEN_KEY);
}
export function isSignedIn() {
	return !!getToken();
}

export class ApiError extends Error {
	constructor(status, body, url) {
		// Include status and URL: "rejected" with no detail is unfixable from
		// a phone, where there are no devtools to open.
		super(body?.detail || body?.error || `HTTP ${status}`);
		this.status = status;
		this.body = body;
		this.url = url;
	}
}

export async function api(path, { method = 'GET', body, signal } = {}) {
	const res = await fetch(getBase() + path, {
		method,
		signal,
		// Passkey sessions ride on an httpOnly cookie; device tokens use the
		// header. Sending both lets one client work either way.
		credentials: 'include',
		headers: {
			Authorization: 'Bearer ' + getToken(),
			...(body ? { 'Content-Type': 'application/json' } : {})
		},
		body: body ? JSON.stringify(body) : undefined
	});
	const text = await res.text();
	let json = null;
	try {
		json = text ? JSON.parse(text) : null;
	} catch {
		/* non-JSON error page */
	}
	if (!res.ok) throw new ApiError(res.status, json, getBase() + path);
	return json;
}

export const Notes = {
	list: (tag) => api('/v1/notes' + (tag ? `?tag=${encodeURIComponent(tag)}` : '')),
	get: (slug) => api(`/v1/notes/${encodeURIComponent(slug)}`),
	put: (slug, payload) => api(`/v1/notes/${encodeURIComponent(slug)}`, { method: 'PUT', body: payload }),
	del: (slug) => api(`/v1/notes/${encodeURIComponent(slug)}`, { method: 'DELETE' }),
	search: (q) => api(`/v1/search?q=${encodeURIComponent(q)}`),
	conflicts: () => api('/v1/conflicts'),
	related: (slug, depth = 1) => api(`/v1/notes/${encodeURIComponent(slug)}/related?depth=${depth}`)
};

export const Cal = {
	range: (from, to) =>
		api(`/v1/events?from=${encodeURIComponent(from)}&to=${encodeURIComponent(to)}`),
	proposed: () => api('/v1/events/proposed'),
	get: (id) => api(`/v1/events/${id}`),
	setState: (id, state) => api(`/v1/events/${id}/state`, { method: 'POST', body: { state } }),
	confirmAll: () => api('/v1/events/confirm-all', { method: 'POST' }),
	patch: (id, p) => api(`/v1/events/${id}`, { method: 'PATCH', body: p }),
	remove: (id) => api(`/v1/events/${id}`, { method: 'DELETE' }),
	create: (e) => api('/v1/events', { method: 'POST', body: e })
};

export const Mail = {
	messages: (limit = 60) => api(`/v1/mail/messages?limit=${limit}`),
	accounts: () => api('/v1/mail/accounts'),
	sync: () => api('/v1/mail/sync', { method: 'POST' }),
	extract: (limit = 10) => api(`/v1/mail/extract?limit=${limit}`, { method: 'POST' })
};

export const Search = {
	// Default mode is hybrid: vector and full-text fused. "semantic" is kept
	// so the difference stays measurable.
	all: (q, mode = 'hybrid', limit = 25) =>
		api(`/v1/search/all?q=${encodeURIComponent(q)}&mode=${mode}&limit=${limit}`),
	embedStats: () => api('/v1/embeddings/stats'),
	backfill: (limit = 25) => api(`/v1/embeddings/backfill?limit=${limit}`, { method: 'POST' })
};

export const Jobs = {
	stats: () => api('/v1/jobs/stats')
};

export const Settings = {
	get: () => api('/v1/settings'),
	set: (patch) => api('/v1/settings', { method: 'PUT', body: patch })
};

export const Sync = {
	since: (seq) => api(`/v1/sync?since=${seq || 0}`)
};

export function slugify(s) {
	return (
		(s || '')
			.toLowerCase()
			.trim()
			.replace(/[^a-z0-9]+/g, '-')
			.replace(/^-|-$/g, '')
			.slice(0, 120) || 'note'
	);
}

// Render a subset of markdown. Deliberately not a full parser: this escapes
// everything first, so note content can never inject HTML.
export function renderMarkdown(src, onLink) {
	const esc = (s) =>
		s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
	let h = esc(src || '');
	h = h.replace(/```([\s\S]*?)```/g, (_, c) => `<pre><code>${c}</code></pre>`);
	h = h.replace(/`([^`\n]+)`/g, '<code>$1</code>');
	h = h.replace(/^###### (.*)$/gm, '<h6>$1</h6>');
	h = h.replace(/^##### (.*)$/gm, '<h5>$1</h5>');
	h = h.replace(/^#### (.*)$/gm, '<h4>$1</h4>');
	h = h.replace(/^### (.*)$/gm, '<h3>$1</h3>');
	h = h.replace(/^## (.*)$/gm, '<h2>$1</h2>');
	h = h.replace(/^# (.*)$/gm, '<h1>$1</h1>');
	h = h.replace(/\[\[([^\]|]+)(?:\|([^\]]*))?\]\]/g, (_, target, label) => {
		const s = slugify(target);
		return `<a class="wiki" data-slug="${s}" href="#/note/${s}">${esc(label || target)}</a>`;
	});
	h = h.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>');
	h = h.replace(/(^|[^*])\*([^*\n]+)\*/g, '$1<em>$2</em>');
	h = h.replace(/^[-*] (.*)$/gm, '<li>$1</li>');
	h = h.replace(/(<li>[\s\S]*?<\/li>)/g, '<ul>$1</ul>');
	h = h.replace(/\n{2,}/g, '</p><p>');
	return `<p>${h}</p>`;
}
