<script>
	import { goto } from '$app/navigation';
	import { render, enhance } from '$lib/markdown.js';
	import { load as loadImage } from '$lib/attach.js';

	let { body = '' } = $props();

	let el = $state(null);
	// render() is synchronous and safe: it escapes, parses, then sanitises with
	// DOMPurify, so note text cannot inject markup.
	let html = $derived(render(body));

	$effect(() => {
		// Re-read html so this re-runs whenever the source changes, then fill in
		// maths, diagrams and images. Failures inside are contained per element.
		html;
		if (el) enhance(el, { loadImage });
	});

	// Wiki links are internal navigation, so they must not reload the app.
	// Everything else keeps normal link behaviour.
	function onClick(e) {
		const a = e.target.closest('a.wiki');
		if (!a) return;
		e.preventDefault();
		goto('/note/' + a.dataset.slug);
	}
</script>

<div bind:this={el} class="rendered" onclick={onClick} role="presentation">
	{@html html}
</div>
