<script>
	import { onMount, onDestroy } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { Notes, renderMarkdown, slugify } from '$lib/api.js';
	import { cache } from '$lib/store.js';
	import { queuePut, queueDelete, state as sync } from '$lib/sync.svelte.js';

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

	let timer;

	async function load() {
		const local = await cache.get(slug);
		if (local) apply(local);
		try {
			const res = await Notes.get(slug);
			apply(res.note);
			backlinks = res.backlinks || [];
			outlinks = res.outlinks || [];
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
		const fresh = await cache.get(slug);
		if (fresh) note = fresh;
	}

	async function remove() {
		if (!confirm('Delete this note?')) return;
		await queueDelete(slug);
		goto('/');
	}

	function onRenderedClick(e) {
		const a = e.target.closest('a.wiki');
		if (!a) return;
		e.preventDefault();
		goto('/note/' + a.dataset.slug);
	}
</script>

<div class="topbar">
	<button class="ghost" onclick={() => goto('/')} aria-label="Back" style="padding:6px 10px">‹</button>
	<h1>{title || slug}</h1>
	{#if saving}<span class="pill">saving…</span>
	{:else if dirty}<span class="pill warn">unsaved</span>{/if}
	<button class="ghost" onclick={() => (editing = !editing)} style="padding:6px 12px">
		{editing ? 'Done' : 'Edit'}
	</button>
</div>

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
	<textarea
		class="editor-body"
		bind:value={body}
		oninput={touch}
		placeholder="Write in markdown. Link with [[another note]]."
	></textarea>
	<div class="row" style="margin-top:14px">
		<button onclick={save} disabled={!dirty}>Save now</button>
		<span class="spacer"></span>
		<button class="danger" onclick={remove}>Delete</button>
	</div>
{:else}
	<div class="row" style="margin-bottom:10px">
		{#each note?.tags || [] as t}<span class="tag">{t}</span>{/each}
	</div>
	<!-- renderMarkdown escapes before formatting, so note text cannot inject HTML -->
	<div class="rendered" onclick={onRenderedClick} role="presentation">
		{@html renderMarkdown(body)}
	</div>

	{#if outlinks.length}
		<h3 style="margin-top:26px;font-size:14px" class="muted">Links to</h3>
		{#each outlinks as l}
			<a class="card" href="/note/{l.slug}" style="padding:10px 13px;min-height:0">
				<div class="row">
					<span>{l.title || l.slug}</span>
					<span class="spacer"></span>
					{#if !l.id}<span class="pill">not created</span>{/if}
				</div>
			</a>
		{/each}
	{/if}

	{#if backlinks.length}
		<h3 style="margin-top:20px;font-size:14px" class="muted">Linked from</h3>
		{#each backlinks as l}
			<a class="card" href="/note/{l.slug}" style="padding:10px 13px;min-height:0">{l.title || l.slug}</a>
		{/each}
	{/if}
{/if}
