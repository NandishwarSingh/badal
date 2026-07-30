<script>
	import { goto } from '$app/navigation';
	import { slugify } from '$lib/api.js';
	import { queuePut } from '$lib/sync.svelte.js';

	let title = $state('');
	let body = $state('');
	let tagsText = $state('');
	let busy = $state(false);

	async function create() {
		if (!title.trim() || busy) return;
		busy = true;
		const slug = slugify(title);
		await queuePut({
			slug,
			title: title.trim(),
			body,
			tags: tagsText.split(',').map((s) => s.trim()).filter(Boolean)
		});
		goto('/note/' + slug);
	}
</script>

<div class="topbar">
	<button class="ghost" onclick={() => goto('/')} style="padding:6px 10px">‹</button>
	<h1>New note</h1>
	<button class="primary" onclick={create} disabled={!title.trim() || busy} style="padding:8px 15px;min-height:38px">
		Create
	</button>
</div>

<!-- svelte-ignore a11y_autofocus -->
<input class="editor-title" bind:value={title} placeholder="Title" autofocus
	onkeydown={(e) => e.key === 'Enter' && create()} />
<input style="margin:8px 0;font-size:14px" bind:value={tagsText} placeholder="tags, comma separated" />
<textarea class="editor-body" bind:value={body}
	placeholder="Write in markdown. Link with [[another note]]."></textarea>
