<script>
	import { api } from '$lib/api.js';
	import { renderMail } from '$lib/mailtext.js';

	let { id, onclose } = $props();

	const EXIT_MS = 220;
	let closing = $state(false);
	let msg = $state(null);
	let err = $state('');

	function close() {
		if (closing) return;
		closing = true;
		setTimeout(() => onclose?.(), EXIT_MS);
	}

	$effect(() => {
		history.pushState({ sheet: true }, '');
		const onPop = () => close();
		window.addEventListener('popstate', onPop);
		return () => {
			window.removeEventListener('popstate', onPop);
			if (history.state?.sheet) history.back();
		};
	});

	$effect(() => {
		api(`/v1/mail/messages/${id}`)
			.then((m) => (msg = m))
			.catch((e) => (err = e.message || 'Could not load.'));
	});

	// Senders are long mailing-list names — "'Director Student Welfare' via
	// M.C.A. - Master of Comp App 2026 Group, Vellore Campus". The quoted part
	// is the human; the rest is list plumbing.
	const who = (m) => {
		const n = (m.from_name || m.from_addr || '').trim();
		const quoted = n.match(/^'([^']+)'/);
		return quoted ? quoted[1] : n.split(' via ')[0] || m.from_addr;
	};
	const via = (m) => {
		const n = m.from_name || '';
		const i = n.indexOf(' via ');
		return i > -1 ? n.slice(i + 5).replace(/,?\s*Vellore Campus$/i, '') : '';
	};
	const initial = (m) => (who(m) || '?').replace(/[^A-Za-z]/g, '').charAt(0).toUpperCase() || '?';

	const when = (iso) =>
		new Date(iso).toLocaleString(undefined, {
			weekday: 'short', day: 'numeric', month: 'long',
			hour: '2-digit', minute: '2-digit'
		});
	const kb = (n) => (n > 1024 ? `${Math.round(n / 1024)} KB` : `${n} B`);
</script>

<div
	class="sheet-scrim"
	class:closing
	role="button"
	tabindex="-1"
	onclick={close}
	onkeydown={(k) => k.key === 'Escape' && close()}
></div>

<div class="sheet" class:closing role="dialog" aria-modal="true">
	<div class="sheet-head"></div>

	{#if err}
		<div class="banner err">{err}</div>
	{:else if !msg}
		<div class="empty">Loading…</div>
	{:else}
		<h2 class="mail-subject">{msg.subject || '(no subject)'}</h2>

		<div class="mail-from">
			<div class="avatar">{initial(msg)}</div>
			<div style="min-width:0;flex:1">
				<div class="mail-who">{who(msg)}</div>
				<div class="muted mail-meta">
					{when(msg.received_at)}{via(msg) ? ` · ${via(msg)}` : ''}
				</div>
			</div>
		</div>

		{#if (msg.attachments || []).length}
			<div class="row" style="margin:12px 0 0">
				{#each msg.attachments as a}
					<span class="attach">
						<svg viewBox="0 0 24 24"><path d="M21 12.5 12.5 21a5 5 0 0 1-7-7l8-8a3.5 3.5 0 0 1 5 5l-8 8a2 2 0 0 1-3-3l7-7" /></svg>
						{a.filename || a.mime}{a.size ? ` · ${kb(a.size)}` : ''}
					</span>
				{/each}
			</div>
		{/if}

		<!-- renderMail escapes before formatting, so mail cannot inject markup -->
		<div class="mail-body">{@html renderMail(msg.body_text)}</div>
	{/if}

	<div class="sheet-actions">
		<button class="sheet-close" onclick={close}>Close</button>
	</div>
</div>
