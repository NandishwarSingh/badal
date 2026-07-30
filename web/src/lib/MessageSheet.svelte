<script>
	import { api } from '$lib/api.js';

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

	const when = (iso) =>
		new Date(iso).toLocaleString(undefined, {
			weekday: 'short', day: 'numeric', month: 'long',
			hour: '2-digit', minute: '2-digit'
		});
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
		<h2>{msg.subject || '(no subject)'}</h2>
		<div class="sheet-when">{msg.from_name || msg.from_addr}</div>
		<div class="muted" style="margin-top:2px">{when(msg.received_at)}</div>

		{#if (msg.attachments || []).length}
			<div class="row" style="margin-top:10px">
				{#each msg.attachments as a}
					<span class="pill">{a.filename || a.mime}</span>
				{/each}
			</div>
		{/if}

		<h4 class="sheet-h">Message</h4>
		<!-- Rendered as preformatted text, not HTML: this is untrusted mail and
		     the original markup has already been stripped at ingest. -->
		<pre class="srcbody" style="max-height:none">{msg.body_text}</pre>
	{/if}

	<div class="sheet-actions">
		<button class="sheet-close" onclick={close}>Close</button>
	</div>
</div>
