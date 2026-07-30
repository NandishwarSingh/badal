<script>
	import { onMount, onDestroy } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { Notes } from '$lib/api.js';
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
	// Preview is side-by-side where there is room and a toggle where there is
	// not. On a phone the keyboard already owns half the screen.
	let preview = $state(false);
	let wide = $state(false);

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

	onMount(() => {
		load();
		// Side-by-side only where it fits; below that the preview replaces the
		// editor rather than squeezing both into a phone width.
		const mq = window.matchMedia('(min-width: 900px)');
		wide = mq.matches;
		const on = (e) => (wide = e.matches);
		mq.addEventListener('change', on);
		return () => mq.removeEventListener('change', on);
	});
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

<div class="topbar">
	<button class="ghost" onclick={() => goto('/')} aria-label="Back" style="padding:6px 10px">‹</button>
	<h1>{title || slug}</h1>
	{#if saving}<span class="pill">saving…</span>
	{:else if dirty}<span class="pill warn">unsaved</span>{/if}
	{#if editing}
		<button
			class="ghost"
			class:on={preview}
			onclick={() => (preview = !preview)}
			style="padding:6px 10px"
			aria-pressed={preview}
			title="Preview">◱</button
		>
	{/if}
	<button class="ghost" onclick={() => (editing = !editing)} style="padding:6px 12px">
		{editing ? 'Done' : 'Edit'}
	</button>
</div>

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
	<div class="editsplit" class:split={wide && preview}>
		<div class="editpane">
			<Editor bind:value={body} {slug} oninput={touch} />
		</div>
		{#if preview}
			<div class="previewpane">
				<Rendered {body} />
			</div>
		{/if}
	</div>
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
