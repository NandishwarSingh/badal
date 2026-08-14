<script>
	// Roadmaps: one per subject, built from uploaded PDFs. This is the list and
	// the create form; a roadmap opens on its own page.
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { Roadmaps } from '$lib/api.js';
	import { meta } from '$lib/store.js';
	import { toast } from '$lib/toast.svelte.js';

	let list = $state([]);
	let loading = $state(true);
	let creating = $state(false); // form open
	let busy = $state(false); // upload in flight
	let subject = $state('');
	let files = $state([]);
	let err = $state('');

	async function load() {
		const cached = await meta.get('roadmaps', null);
		if (cached) {
			list = cached;
			loading = false;
		}
		try {
			const res = await Roadmaps.list();
			list = res.roadmaps || [];
			await meta.set('roadmaps', list);
		} catch {
			// offline: cache already shown
		} finally {
			loading = false;
		}
	}
	onMount(load);

	function pick(e) {
		files = Array.from(e.target.files || []);
	}

	async function create() {
		if (!subject.trim() || files.length === 0) {
			err = 'A subject and at least one file are needed.';
			return;
		}
		busy = true;
		err = '';
		try {
			const res = await Roadmaps.create(subject.trim(), subject.trim(), files);
			toast('Roadmap built');
			creating = false;
			subject = '';
			files = [];
			await load();
			if (res.roadmap?.id) goto(`/roadmaps/${res.roadmap.id}`);
		} catch (e) {
			err = e?.message || 'Could not build the roadmap.';
		} finally {
			busy = false;
		}
	}

	const pct = (r) => (r.leaves ? Math.round((r.done / r.leaves) * 100) : 0);
</script>

<div class="pagehead">
	<button class="iconbtn" onclick={() => goto('/')} aria-label="Back">
		<svg viewBox="0 0 24 24"><path d="M15 5l-7 7 7 7" /></svg>
	</button>
	<h1>Roadmaps</h1>
	<span class="spacer"></span>
	<button class="iconbtn" onclick={() => (creating = !creating)} aria-label="New roadmap">
		<svg viewBox="0 0 24 24"><path d="M12 5v14 M5 12h14" /></svg>
	</button>
</div>

{#if creating}
	<div class="rm-form">
		<label class="fieldlabel" for="rm-subject">Subject</label>
		<input id="rm-subject" bind:value={subject} placeholder="e.g. Machine Learning" />

		<label class="fieldlabel" for="rm-files">Topic PDFs</label>
		<input
			id="rm-files"
			type="file"
			multiple
			accept=".pdf,.doc,.docx,.ppt,.pptx,.odt,.rtf,.epub"
			onchange={pick}
		/>
		{#if files.length}<p class="muted rm-files">{files.length} file{files.length > 1 ? 's' : ''} selected</p>{/if}

		{#if err}<div class="banner err" style="margin-top:10px">{err}</div>{/if}

		<div class="resolve" style="margin-top:12px">
			<span class="spacer"></span>
			<button class="primary" onclick={create} disabled={busy || !subject.trim() || !files.length}>
				{#if busy}<span class="spinner"></span> Building…{:else}Build roadmap{/if}
			</button>
		</div>
		{#if busy}
			<p class="muted rm-note">Parsing the documents and building the checklist. A big PDF can take a little while.</p>
		{/if}
	</div>
{/if}

{#if loading && list.length === 0}
	<p class="muted" style="padding:12px 2px">Loading…</p>
{:else if list.length === 0}
	<div class="empty">
		<p>No roadmaps yet.</p>
		<span class="muted">Tap + to upload a subject's PDFs and turn them into a checklist.</span>
	</div>
{:else}
	<div class="group">
		{#each list as r (r.id)}
			<button class="grow" onclick={() => goto(`/roadmaps/${r.id}`)}>
				<span class="gbody">
					<span class="gtitle">{r.title}</span>
					<span class="gsub">
						{#if r.status === 'processing'}building…{:else if r.status === 'failed'}failed{:else}{r.done}/{r.leaves} done{/if}
					</span>
				</span>
				{#if r.status === 'ready' && r.leaves}
					<span class="rm-pct">{pct(r)}%</span>
				{/if}
			</button>
		{/each}
	</div>
{/if}

<style>
	.rm-form { margin: 4px 0 18px; display: flex; flex-direction: column; }
	.rm-files { margin: 6px 0 0; font-size: 12px; }
	.rm-note { margin: 8px 0 0; font-size: 12px; }
	.rm-pct {
		flex: 0 0 auto; font-size: 13px; font-weight: 620;
		color: var(--accent); font-variant-numeric: tabular-nums;
	}
</style>
