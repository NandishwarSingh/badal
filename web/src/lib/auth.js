// Passkey (WebAuthn) client.
//
// Nothing durable is stored in the browser: the credential lives in the
// Secure Enclave / Android Keystore, and the session is an httpOnly cookie
// script cannot read. An XSS here has nothing to walk off with.

const b64uToBuf = (s) => {
	const pad = '='.repeat((4 - (s.length % 4)) % 4);
	const bin = atob((s + pad).replace(/-/g, '+').replace(/_/g, '/'));
	return Uint8Array.from(bin, (c) => c.charCodeAt(0)).buffer;
};

const bufToB64u = (b) =>
	btoa(String.fromCharCode(...new Uint8Array(b)))
		.replace(/\+/g, '-')
		.replace(/\//g, '_')
		.replace(/=+$/, '');

export function passkeysSupported() {
	return (
		typeof window !== 'undefined' &&
		!!window.PublicKeyCredential &&
		!!navigator.credentials?.create
	);
}

export async function platformAuthenticatorAvailable() {
	if (!passkeysSupported()) return false;
	try {
		return await PublicKeyCredential.isUserVerifyingPlatformAuthenticatorAvailable();
	} catch {
		return false;
	}
}

async function post(path, body, headers = {}) {
	const res = await fetch(path, {
		method: 'POST',
		credentials: 'include', // the session cookie is the whole point
		headers: { 'Content-Type': 'application/json', ...headers },
		body: body ? JSON.stringify(body) : undefined
	});
	const j = await res.json().catch(() => null);
	if (!res.ok) throw new Error(j?.detail || j?.error || `HTTP ${res.status}`);
	return j;
}

export async function authState() {
	const res = await fetch('/v1/auth/state', { credentials: 'include' });
	return res.json();
}

/** Enrol a passkey. `secret` is required only for the very first one. */
export async function registerPasskey(label, secret) {
	const headers = secret ? { 'X-Badal-Enrol': secret } : {};
	const { ceremony, options } = await post('/v1/auth/register/begin', null, headers);
	const pub = options.publicKey;

	pub.challenge = b64uToBuf(pub.challenge);
	pub.user.id = b64uToBuf(pub.user.id);
	if (pub.excludeCredentials) {
		pub.excludeCredentials = pub.excludeCredentials.map((c) => ({ ...c, id: b64uToBuf(c.id) }));
	}

	const cred = await navigator.credentials.create({ publicKey: pub });
	if (!cred) throw new Error('cancelled');

	const payload = {
		id: cred.id,
		rawId: bufToB64u(cred.rawId),
		type: cred.type,
		clientExtensionResults: cred.getClientExtensionResults?.() ?? {},
		response: {
			clientDataJSON: bufToB64u(cred.response.clientDataJSON),
			attestationObject: bufToB64u(cred.response.attestationObject),
			transports: cred.response.getTransports?.() ?? []
		}
	};
	const q = new URLSearchParams({ ceremony, label: label || '' });
	return post(`/v1/auth/register/finish?${q}`, payload, headers);
}

export async function loginWithPasskey() {
	const { ceremony, options } = await post('/v1/auth/login/begin');
	const pub = options.publicKey;

	pub.challenge = b64uToBuf(pub.challenge);
	if (pub.allowCredentials) {
		pub.allowCredentials = pub.allowCredentials.map((c) => ({ ...c, id: b64uToBuf(c.id) }));
	}

	const assertion = await navigator.credentials.get({ publicKey: pub });
	if (!assertion) throw new Error('cancelled');

	const payload = {
		id: assertion.id,
		rawId: bufToB64u(assertion.rawId),
		type: assertion.type,
		clientExtensionResults: assertion.getClientExtensionResults?.() ?? {},
		response: {
			clientDataJSON: bufToB64u(assertion.response.clientDataJSON),
			authenticatorData: bufToB64u(assertion.response.authenticatorData),
			signature: bufToB64u(assertion.response.signature),
			userHandle: assertion.response.userHandle
				? bufToB64u(assertion.response.userHandle)
				: null
		}
	};
	return post(`/v1/auth/login/finish?ceremony=${ceremony}`, payload);
}

export async function logout() {
	await post('/v1/auth/logout').catch(() => {});
}

export async function listCredentials() {
	// Via api() so a device-token session works here too, not just a passkey.
	const { api } = await import('./api.js');
	return api('/v1/auth/credentials')
		.then((r) => r.credentials || [])
		.catch(() => []);
}
