<script>
	import Editor from '$lib/Editor.svelte';
	import Icon from '$lib/Icon.svelte';
	import { Tasks } from '$lib/api.js';
	import { queueTask } from '$lib/sync.svelte.js';
	import { upload, isImage } from '$lib/attach.js';
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { slugify } from '$lib/api.js';
	import { queuePut } from '$lib/sync.svelte.js';
	import { toast } from '$lib/toast.svelte.js';

	let title = $state('');
	let body = $state('');
	let tagsText = $state('');
	let busy = $state(false);
	let shared = $state(false);
	let ta;
	// null until you have said what this is. Anything shared in skips the
	// question -- the answer is already implied by what arrived.
	let kind = $state(null);
	let taskTitle = $state('');
	let picker = $state(null);
	let uploading = $state(false);

	async function addTask() {
		const t = taskTitle.trim();
		if (!t || busy) return;
		busy = true;
		await queueTask({
			id: crypto.randomUUID(),
			title: t,
			notes: '',
			state: 'open',
			priority: 0,
			all_day: true,
			reminders: []
		});
		toast('Added');
		goto('/', { replaceState: true });
	}

	// A photo becomes a note holding it. There is no separate place for a
	// loose image, and inventing one would be a whole new kind of thing.
	async function addPhoto(files) {
		const f = [...files].find(isImage);
		if (!f) return;
		uploading = true;
		try {
			const name = (f.name || 'Photo').replace(/\.[a-z0-9]+$/i, '');
			const slug = slugify(name) || 'photo-' + Date.now().toString(36);
			const { ref } = await upload(f, { noteSlug: slug });
			await queuePut({ slug, title: name, body: `![${name}](${ref})\n`, tags: ['photo'] });
			toast('Saved');
			goto('/note/' + slug, { replaceState: true });
		} catch (e) {
			toast(e.message || 'Could not save that image');
			uploading = false;
		}
	}

	onMount(() => {
		// Web Share Target (Android) and the Home Screen shortcut both land
		// here. Anything shared in becomes a draft, never a silent write.
		const q = page.url.searchParams;
		const st = q.get('title'), sx = q.get('text'), su = q.get('url');
		if (st || sx || su) {
			shared = true;
			kind = 'note';
			title = st || (sx || '').split('\n')[0].slice(0, 70) || 'Shared';
			body = [sx && sx !== st ? sx : '', su ? `\n\n${su}` : ''].filter(Boolean).join('');
			tagsText = 'inbox';
		}
		if (q.get('type')) kind = q.get('type');
		if (q.get('focus') || shared) setTimeout(() => ta?.focus(), 120);
	});

	async function create() {
		if (!title.trim() || busy) return;
		busy = true;
		const slug = slugify(title);
		// Optimistic: this writes to IndexedDB and returns before the network
		// is involved, so the next screen is already correct.
		await queuePut({
			slug, title: title.trim(), body,
			tags: tagsText.split(',').map((s) => s.trim()).filter(Boolean)
		});
		toast('Saved');
		goto('/note/' + slug, { replaceState: true });
	}
</script>

<div class="pagehead">
	<button class="iconbtn" onclick={() => (kind ? (kind = null) : goto('/'))} aria-label="Back">
		<svg viewBox="0 0 24 24"><path d="M15 5l-7 7 7 7" /></svg>
	</button>
	<h1 style="font-size:24px">
		{shared ? 'Save shared' : kind === 'task' ? 'New task' : kind === 'note' ? 'New note' : 'Capture'}
	</h1>
	<span class="spacer"></span>
	{#if kind === 'note'}
		<button
			class="primary"
			onclick={create}
			disabled={!title.trim() || busy}
			style="padding:9px 18px"
		>
			{#if busy}<span class="spinner"></span>{/if} Save
		</button>
	{/if}
</div>

{#if !kind}
	<div class="group">
		<button class="grow" onclick={() => { kind = 'note'; setTimeout(() => ta?.focus(), 60); }}>
			<span class="gtile slate"><Icon name="note" size={19} stroke={2.1} /></span>
			<span class="gbody">
				<span class="gtitle">Note</span>
				<span class="gsub">Markdown, maths, diagrams, images</span>
			</span>
			<span class="chev"><Icon name="chevron" size={15} stroke={2} /></span>
		</button>
		<button class="grow" onclick={() => (kind = 'task')}>
			<span class="gtile green"><Icon name="task" size={19} stroke={2.1} /></span>
			<span class="gbody">
				<span class="gtitle">Task</span>
				<span class="gsub">Something to do, with a reminder</span>
			</span>
			<span class="chev"><Icon name="chevron" size={15} stroke={2} /></span>
		</button>
		<button class="grow" onclick={() => picker?.click()} disabled={uploading}>
			<span class="gtile purple"><Icon name="event" size={19} stroke={2.1} /></span>
			<span class="gbody">
				<span class="gtitle">Photo</span>
				<span class="gsub">
					{uploading ? 'Uploading…' : 'Saves as a note holding the picture'}
				</span>
			</span>
			{#if uploading}
				<span class="spinner"></span>
			{:else}
				<span class="chev"><Icon name="chevron" size={15} stroke={2} /></span>
			{/if}
		</button>
	</div>

	<input
		bind:this={picker}
		type="file"
		accept="image/*"
		hidden
		onchange={(e) => {
			addPhoto(e.currentTarget.files);
			e.currentTarget.value = '';
		}}
	/>
{:else if kind === 'task'}
	<form class="quickadd" onsubmit={(e) => (e.preventDefault(), addTask())}>
		<!-- svelte-ignore a11y_autofocus -->
		<input bind:value={taskTitle} placeholder="What needs doing" autofocus enterkeyhint="done" />
		<button class="primary" type="submit" disabled={!taskTitle.trim() || busy}>Add</button>
	</form>
	<p class="muted" style="margin:2px 2px">
		It lands in Now. Open it there to set a date, a reminder or a repeat.
	</p>
{:else}
	<!-- svelte-ignore a11y_autofocus -->
	<input
		class="editor-title"
		bind:value={title}
		placeholder="Title"
		autofocus={!shared}
		onkeydown={(e) => e.key === 'Enter' && (e.preventDefault(), ta?.focus())}
	/>
	<input
		style="margin:9px 0;font-size:14px"
		bind:value={tagsText}
		placeholder="tags, comma separated"
	/>
	<Editor bind:this={ta} bind:value={body} slug="inbox" />
{/if}
