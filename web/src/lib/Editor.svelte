<script>
	import { upload, isImage } from '$lib/attach.js';
	import { toast } from '$lib/toast.svelte.js';

	let { value = $bindable(''), slug = 'inbox', oninput } = $props();

	let ta = $state(null);
	let picker = $state(null);
	let uploading = $state(0);

	// The capture screen focuses the body after the title, and bind:this on a
	// component yields the instance rather than the textarea inside it.
	export function focus() {
		ta?.focus();
	}

	// Insert at the caret rather than appending, and leave the caret after the
	// inserted line so typing carries on where you would expect.
	function insert(md) {
		const s = ta?.selectionStart ?? value.length;
		const e = ta?.selectionEnd ?? value.length;
		const pad = s > 0 && value[s - 1] !== '\n' ? '\n\n' : '';
		const text = pad + md + '\n';
		value = value.slice(0, s) + text + value.slice(e);
		oninput?.();
		requestAnimationFrame(() => {
			ta?.focus();
			ta?.setSelectionRange(s + text.length, s + text.length);
		});
	}

	async function addFiles(files) {
		const imgs = [...files].filter(isImage);
		if (!imgs.length) return;
		uploading += imgs.length;
		for (const f of imgs) {
			try {
				const { ref } = await upload(f, { noteSlug: slug });
				const alt = (f.name || 'image').replace(/\.[a-z0-9]+$/i, '').slice(0, 60);
				insert(`![${alt}](${ref})`);
			} catch (err) {
				toast(err.message || 'Upload failed');
			} finally {
				uploading--;
			}
		}
	}

	function onPaste(e) {
		const files = [...(e.clipboardData?.files || [])];
		if (files.some(isImage)) {
			e.preventDefault();
			addFiles(files);
		}
	}

	function onDrop(e) {
		const files = [...(e.dataTransfer?.files || [])];
		if (files.some(isImage)) {
			e.preventDefault();
			addFiles(files);
		}
	}
</script>

<textarea
	bind:this={ta}
	class="editor-body"
	bind:value
	oninput={() => oninput?.()}
	onpaste={onPaste}
	ondrop={onDrop}
	spellcheck="true"
	placeholder="Write in markdown. Link with [[another note]]."
></textarea>

<!-- Pasting and dropping already work; the button is for a phone, where there
     is no drag and the photo picker is the only way in. -->
<button class="addimg" onclick={() => picker?.click()} disabled={uploading > 0}>
	{#if uploading}
		<span class="spinner"></span> Uploading {uploading} image{uploading > 1 ? 's' : ''}…
	{:else}
		Add image
	{/if}
</button>

<input
	bind:this={picker}
	type="file"
	accept="image/*"
	multiple
	hidden
	onchange={(e) => {
		addFiles(e.currentTarget.files);
		e.currentTarget.value = '';
	}}
/>
