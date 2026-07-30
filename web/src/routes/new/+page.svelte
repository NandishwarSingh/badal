<script>
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

	onMount(() => {
		// Web Share Target (Android) and the Home Screen shortcut both land
		// here. Anything shared in becomes a draft, never a silent write.
		const q = page.url.searchParams;
		const st = q.get('title'), sx = q.get('text'), su = q.get('url');
		if (st || sx || su) {
			shared = true;
			title = st || (sx || '').split('\n')[0].slice(0, 70) || 'Shared';
			body = [sx && sx !== st ? sx : '', su ? `\n\n${su}` : ''].filter(Boolean).join('');
			tagsText = 'inbox';
		}
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

<div class="topbar">
	<button class="ghost" onclick={() => goto('/')} style="padding:6px 10px;min-height:0">‹</button>
	<h1>{shared ? 'Save shared' : 'Capture'}</h1>
	<button class="primary" onclick={create} disabled={!title.trim() || busy}
		style="padding:8px 16px;min-height:38px">
		{#if busy}<span class="spinner"></span>{/if} Save
	</button>
</div>

<!-- svelte-ignore a11y_autofocus -->
<input class="editor-title" bind:value={title} placeholder="Title" autofocus={!shared}
	onkeydown={(e) => e.key === 'Enter' && (e.preventDefault(), ta?.focus())} />
<input style="margin:9px 0;font-size:14px" bind:value={tagsText} placeholder="tags, comma separated" />
<textarea bind:this={ta} class="editor-body" bind:value={body}
	placeholder="Markdown. Link with [[another note]]."></textarea>
