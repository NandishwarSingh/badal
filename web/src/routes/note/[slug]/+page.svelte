<script>
	import { onMount, onDestroy } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { Notes, api } from '$lib/api.js';
	import { mergeBoth } from '$lib/diff.js';
	import Editor from '$lib/Editor.svelte';
	import Rendered from '$lib/Rendered.svelte';
	import { cache } from '$lib/store.js';
	import { queuePut, queueDelete, state as sync } from '$lib/sync.svelte.js';
	import { toast } from '$lib/toast.svelte.js';
	import ConflictCard from '$lib/ConflictCard.svelte';

	let slug = $derived(page.params.slug);
	let note = $state(null);
	let title = $state('');
	let body = $state('');
	let tagsText = $state('');
	let editing = $state(false);
	let dirty = $state(false);
	let saving = $state(false);
	let backlinks = $state([]);
	let outlinks = $state([]);
	let notFound = $state(false);
	let conflicts = $state([]);
	let bulkBusy = $state('');

	// Resolve every conflict on this note at once.
	//
	// Sequential, not parallel: each resolution bumps the note's version, so
	// the next one has to be computed against the note the previous one just
	// produced. Firing them together would make all but the first stale.
	async function resolveAll(kind) {
		bulkBusy = kind;
		try {
			let current = note;
			for (const c of [...conflicts]) {
				let payload;
				if (kind === 'mine') payload = { keep: 'current' };
				else if (kind === 'theirs') payload = { keep: 'incoming' };
				else
					payload = {
						keep: 'custom',
						title: c.incoming_title || current.title,
						body: mergeBoth(current.body || '', c.incoming_body || ''),
						tags: current.tags || []
					};
				const res = await api(`/v1/conflicts/${c.id}/resolve`, { method: 'POST', body: payload });
				current = res.note;
			}
			note = current;
			title = current.title;
			body = current.body;
			tagsText = (current.tags || []).join(', ');
			dirty = false;
			conflicts = [];
			await cache.put({ ...current, _dirty: false });
			toast(
				kind === 'mine'
					? 'Kept this version'
					: kind === 'theirs'
						? 'Applied their edits'
						: 'Merged everything'
			);
		} catch (e) {
			err = e.message || 'Could not resolve.';
		} finally {
			bulkBusy = '';
		}
	}

	let timer;

	async function load() {
		const local = await cache.get(slug);
		if (local) apply(local);
		try {
			const res = await Notes.get(slug);
			apply(res.note);
			backlinks = res.backlinks || [];
			outlinks = res.outlinks || [];
			conflicts = res.conflicts || [];
			await cache.put({ ...res.note, _dirty: false });
		} catch (e) {
			if (!local) notFound = true;
		}
	}

	function apply(n) {
		note = n;
		if (!dirty) {
			title = n.title;
			body = n.body;
			tagsText = (n.tags || []).join(', ');
		}
	}

	onMount(load);
	onDestroy(() => {
		clearTimeout(timer);
		if (dirty) void save();
	});

	function touch() {
		dirty = true;
		clearTimeout(timer);
		// Autosave rather than a Save button: on a phone you leave by swiping,
		// and an unsaved-changes prompt there is just a way to lose work.
		timer = setTimeout(save, 1200);
	}

	async function save() {
		if (!dirty || !title.trim()) return;
		saving = true;
		const tags = tagsText
			.split(',')
			.map((s) => s.trim())
			.filter(Boolean);
		await queuePut({
			slug,
			title: title.trim(),
			body,
			tags,
			version: note?.version ?? undefined
		});
		dirty = false;
		saving = false;
		toast('Saved');
		const fresh = await cache.get(slug);
		if (fresh) note = fresh;
	}

	async function remove() {
		if (!confirm('Delete this note?')) return;
		await queueDelete(slug);
		toast('Deleted');
		goto('/');
	}

</script>

<div class="pagehead">
	<button class="iconbtn" onclick={() => goto('/notes')} aria-label="Back">
		<svg viewBox="0 0 24 24"><path d="M15 5l-7 7 7 7" /></svg>
	</button>
	<h1 style="font-size:22px;flex:1;min-width:0">{title || slug}</h1>
	{#if saving}<span class="pill">saving…</span>{:else if dirty}<span class="pill warn">unsaved</span>{/if}
	<button class="iconbtn" onclick={() => (editing = !editing)} aria-label={editing ? 'Done' : 'Edit'}>
		{#if editing}
			<svg viewBox="0 0 24 24"><path d="M4 12.5l5 5L20 6.5" /></svg>
		{:else}
			<svg viewBox="0 0 24 24"><path d="M4 20h4l10-10-4-4L4 16z M13.5 6.5l4 4" /></svg>
		{/if}
	</button>
</div>

<!-- With more than one conflict, working through the cards one at a time is
     the tedious path; these settle the whole note in a tap. -->
{#if conflicts.length > 1}
	<div class="bulkconf">
		<span class="bulklabel">{conflicts.length} conflicting edits</span>
		<div class="bulkbtns">
			<button onclick={() => resolveAll('mine')} disabled={!!bulkBusy}>
				{#if bulkBusy === 'mine'}<span class="spinner"></span>{/if} Keep all mine
			</button>
			<button onclick={() => resolveAll('theirs')} disabled={!!bulkBusy}>
				{#if bulkBusy === 'theirs'}<span class="spinner"></span>{/if} Use all theirs
			</button>
			<button class="primary" onclick={() => resolveAll('both')} disabled={!!bulkBusy}>
				{#if bulkBusy === 'both'}<span class="spinner"></span>{/if} Merge all
			</button>
		</div>
	</div>
{/if}

{#each conflicts as c (c.id)}
	<ConflictCard
		conflict={c}
		{note}
		onresolved={(n) => {
			note = n;
			title = n.title;
			body = n.body;
			tagsText = (n.tags || []).join(', ');
			conflicts = conflicts.filter((x) => x.id !== c.id);
		}}
	/>
{/each}

{#if notFound}
	<div class="empty">
		<p>No note called <strong>{slug}</strong>.</p>
		<button class="primary" onclick={() => { notFound = false; editing = true; title = slug.replace(/-/g, ' '); }}>
			Create it
		</button>
	</div>
{:else if editing}
	<input class="editor-title" bind:value={title} oninput={touch} placeholder="Title" />
	<input
		style="margin:8px 0;font-size:14px"
		bind:value={tagsText}
		oninput={touch}
		placeholder="tags, comma separated"
	/>
	<Editor bind:value={body} {slug} oninput={touch} />
	<div class="row" style="margin-top:14px">
		<button onclick={save} disabled={!dirty}>Save now</button>
		<span class="spacer"></span>
		<button class="danger" onclick={remove}>Delete</button>
	</div>
{:else}
	<div class="row" style="margin-bottom:10px">
		{#each note?.tags || [] as t}<span class="tag">{t}</span>{/each}
	</div>
	<Rendered {body} />

	{#if outlinks.length}
		<div class="sectionhead"><h2>Links to</h2></div>
		<div class="group">
			{#each outlinks as l}
				<a class="grow" href="/note/{l.slug}">
					<span class="gtile blue">
						<svg viewBox="0 0 24 24"><path d="M10 13a5 5 0 0 0 7 0l3-3a5 5 0 0 0-7-7l-1 1" /><path d="M14 11a5 5 0 0 0-7 0l-3 3a5 5 0 0 0 7 7l1-1" /></svg>
					</span>
					<span class="gbody">
						<span class="gtitle">{l.title || l.slug}</span>
						{#if !l.id}<span class="gsub">not created yet</span>{/if}
					</span>
					<span class="chev">›</span>
				</a>
			{/each}
		</div>
	{/if}

	{#if backlinks.length}
		<div class="sectionhead"><h2>Linked from</h2></div>
		<div class="group">
			{#each backlinks as l}
				<a class="grow" href="/note/{l.slug}">
					<span class="gtile purple">
						<svg viewBox="0 0 24 24"><path d="M10 13a5 5 0 0 0 7 0l3-3a5 5 0 0 0-7-7l-1 1" /><path d="M14 11a5 5 0 0 0-7 0l-3 3a5 5 0 0 0 7 7l1-1" /></svg>
					</span>
					<span class="gbody">
						<span class="gtitle">{l.title || l.slug}</span>
						{#if !l.id}<span class="gsub">not created yet</span>{/if}
					</span>
					<span class="chev">›</span>
				</a>
			{/each}
		</div>
	{/if}
{/if}
