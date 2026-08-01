<script>
	import { askStream } from '$lib/api.js';
	import Icon from '$lib/Icon.svelte';

	let { subjects = [] } = $props();

	let q = $state('');
	let question = $state('');
	let answer = $state('');
	let sources = $state([]);
	let busy = $state(false);
	let err = $state('');
	let ctl = null;

	async function ask() {
		const text = q.trim();
		if (!text || busy) return;
		ctl?.abort();
		ctl = new AbortController();
		question = text;
		answer = '';
		sources = [];
		err = '';
		busy = true;
		q = '';
		try {
			await askStream(
				text,
				(e) => {
					if (e.sources) sources = e.sources;
					if (e.delta) answer += e.delta;
					if (e.error) err = e.error;
					if (e.done) busy = false;
				},
				{ subjects, signal: ctl.signal }
			);
		} catch (x) {
			if (x.name !== 'AbortError') err = x.message || 'Could not reach the model.';
		} finally {
			busy = false;
		}
	}

	function stop() {
		ctl?.abort();
		busy = false;
	}

	// The answer arrives with [1] [2] inline. Splitting on them lets each one
	// become a tappable chip while the rest of the text keeps streaming.
	const parts = $derived.by(() => {
		const out = [];
		const re = /\[(\d+)\]/g;
		let last = 0;
		let m;
		while ((m = re.exec(answer))) {
			if (m.index > last) out.push({ text: answer.slice(last, m.index) });
			out.push({ cite: Number(m[1]) });
			last = m.index + m[0].length;
		}
		if (last < answer.length) out.push({ text: answer.slice(last) });
		return out;
	});

	function href(s) {
		if (s.source_type === 'note' && s.slug) return `/note/${s.slug}`;
		if (s.source_type === 'event') return `/calendar?event=${s.source_id}`;
		if (s.source_type === 'message') return `/calendar?mail=${s.source_id}`;
		if (s.source_type === 'task') return `/?task=${s.source_id}`;
		return '/';
	}
	const mark = { note: 'note', message: 'mail', event: 'event', task: 'task' };
	const byN = $derived(Object.fromEntries(sources.map((s) => [s.n, s])));

	function go(n) {
		const s = byN[n];
		if (s) window.location.href = href(s);
	}
</script>

<form class="quickadd askbar" onsubmit={(e) => (e.preventDefault(), ask())}>
	<input
		bind:value={q}
		placeholder="Ask about your notes, mail and tasks…"
		enterkeyhint="send"
		aria-label="Ask a question"
	/>
	{#if busy}
		<button type="button" onclick={stop}>Stop</button>
	{:else}
		<button class="primary" type="submit" disabled={!q.trim()}>Ask</button>
	{/if}
</form>

{#if !question && !busy}
	<div class="empty">
		<p style="font-size:17px;color:var(--text);margin-bottom:6px">Ask anything</p>
		<span class="muted">
			It answers only from what you actually have, and shows you which source each part
			came from. If nothing covers it, it says so.
		</span>
	</div>
{/if}

{#if question}
	<div class="askq">{question}</div>

	{#if err}<div class="banner err">{err}</div>{/if}

	<div class="answer">
		{#each parts as p}
			{#if p.cite}<button class="cite" onclick={() => go(p.cite)}>{p.cite}</button
				>{:else}{p.text}{/if}
		{/each}
		{#if busy}<span class="caret"></span>{/if}
	</div>

	{#if sources.length}
		<div class="sectionhead"><h2>Sources</h2></div>
		<div class="group marked">
			{#each sources as s (s.n)}
				<a class="grow rich" href={href(s)}>
					<span class="gmark {s.source_type}"><Icon name={mark[s.source_type] ?? 'note'} size={17} /></span>
					<span class="gbody">
						<span class="gmeta"><span>{s.n}</span></span>
						<span class="gtitle">{s.title || '(untitled)'}</span>
						{#if s.snippet}<span class="gtext">{s.snippet}</span>{/if}
					</span>
				</a>
			{/each}
		</div>
	{/if}
{/if}
