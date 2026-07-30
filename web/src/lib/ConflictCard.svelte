<script>
	import { diffLines, collapse, diffStats } from '$lib/diff.js';
	import { api } from '$lib/api.js';
	import { toast } from '$lib/toast.svelte.js';

	let { conflict, note, onresolved } = $props();

	let busy = $state('');
	let err = $state('');
	let merging = $state(false);
	let mergeText = $state('');

	const rows = $derived(diffLines(note.body || '', conflict.incoming_body || ''));
	const shown = $derived(collapse(rows, 2));
	const stats = $derived(diffStats(rows));
	const titleChanged = $derived(conflict.incoming_title !== note.title);

	async function resolve(keep) {
		busy = keep;
		err = '';
		try {
			const body = { keep };
			if (keep === 'custom') {
				body.title = conflict.incoming_title || note.title;
				body.body = mergeText;
				body.tags = note.tags || [];
			}
			const res = await api(`/v1/conflicts/${conflict.id}/resolve`, { method: 'POST', body });
			toast(
				keep === 'current' ? 'Kept this version' : keep === 'incoming' ? 'Applied their edit' : 'Merged'
			);
			onresolved?.(res.note);
		} catch (e) {
			err = e.message || 'Could not resolve.';
		} finally {
			busy = '';
		}
	}

	function startMerge() {
		// Seed with the current text plus the incoming lines marked, so the
		// starting point is a real document rather than an empty box.
		mergeText = rows
			.filter((r) => r.type !== 'del')
			.map((r) => r.text)
			.join('\n');
		merging = true;
	}

	const when = (iso) => new Date(iso).toLocaleString(undefined, { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' });
</script>

<div class="conflict">
	<div class="conflict-head">
		<span class="conflict-dot"></span>
		<strong>Conflicting edit</strong>
		<span class="spacer"></span>
		<span class="diffstat">
			<span class="plus">+{stats.added}</span><span class="minus">−{stats.removed}</span>
		</span>
	</div>

	<p class="muted" style="margin:0 0 11px">
		Another device edited this while you were on v{conflict.base_version} (now v{conflict.server_version}),
		{when(conflict.created_at)}.
	</p>

	{#if titleChanged}
		<div class="diff">
			<div class="dline del"><span class="sign">−</span><span class="txt">{note.title}</span></div>
			<div class="dline add"><span class="sign">+</span><span class="txt">{conflict.incoming_title}</span></div>
		</div>
		<p class="muted" style="margin:6px 0 11px;font-size:12px">Title differs</p>
	{/if}

	{#if !merging}
		<div class="diff" data-noswipe>
			{#each shown as r}
				{#if r.type === 'gap'}
					<div class="dline gap">⋯ {r.count} unchanged line{r.count > 1 ? 's' : ''}</div>
				{:else}
					<div class="dline {r.type}">
						<span class="sign">{r.type === 'add' ? '+' : r.type === 'del' ? '−' : ' '}</span>
						<span class="txt">{r.text || ' '}</span>
					</div>
				{/if}
			{/each}
		</div>

		<div class="legend">
			<span><i class="sw del"></i> this device</span>
			<span><i class="sw add"></i> the other device</span>
		</div>

		{#if err}<div class="banner err" style="margin-top:10px">{err}</div>{/if}

		<div class="resolve">
			<button onclick={() => resolve('current')} disabled={!!busy}>
				{#if busy === 'current'}<span class="spinner"></span>{/if} Keep this
			</button>
			<button onclick={() => resolve('incoming')} disabled={!!busy}>
				{#if busy === 'incoming'}<span class="spinner"></span>{/if} Use theirs
			</button>
			<button class="primary" onclick={startMerge} disabled={!!busy}>Merge…</button>
		</div>
	{:else}
		<p class="muted" style="margin:0 0 8px;font-size:13px">
			Edit freely — this replaces the note and closes the conflict.
		</p>
		<textarea bind:value={mergeText} rows="12" data-noswipe></textarea>
		{#if err}<div class="banner err" style="margin-top:10px">{err}</div>{/if}
		<div class="resolve">
			<button onclick={() => (merging = false)} disabled={!!busy}>Back</button>
			<button class="primary" onclick={() => resolve('custom')} disabled={!!busy || !mergeText.trim()}>
				{#if busy === 'custom'}<span class="spinner"></span>{/if} Save merged
			</button>
		</div>
	{/if}
</div>
