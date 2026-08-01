<script>
	import { askStream } from '$lib/api.js';
	import Icon from '$lib/Icon.svelte';
	import { render } from '$lib/markdown.js';

	let { subjects = [] } = $props();

	let q = $state('');
	// The whole exchange, so a follow-up can resolve "it" against what came
	// before -- and so you can read back what you asked.
	let turns = $state([]);
	let live = $state(null); // the answer currently streaming
	let busy = $state(false);
	let err = $state('');
	let ctl = null;

	async function ask() {
		const text = q.trim();
		if (!text || busy) return;
		ctl?.abort();
		ctl = new AbortController();
		err = '';
		busy = true;
		q = '';

		// Only completed exchanges go back as history; the turn being asked
		// is the question, not part of the context.
		const history = turns.flatMap((t) => [
			{ role: 'user', content: t.question },
			{ role: 'assistant', content: t.answer }
		]);

		live = { question: text, answer: '', sources: [], queries: [] };
		try {
			await askStream(
				text,
				(e) => {
					if (e.queries) live = { ...live, queries: e.queries };
					if (e.sources) live = { ...live, sources: e.sources };
					if (e.delta) live = { ...live, answer: live.answer + e.delta };
					if (e.error) err = e.error;
					if (e.done) finish();
				},
				{ history, subjects, signal: ctl.signal }
			);
			finish();
		} catch (x) {
			if (x.name !== 'AbortError') err = x.message || 'Could not reach the model.';
			finish();
		}
	}

	function finish() {
		if (!busy) return;
		busy = false;
		if (live && live.answer.trim()) {
			turns = [...turns, live];
			live = null;
		}
	}

	function stop() {
		ctl?.abort();
		finish();
	}

	function reset() {
		ctl?.abort();
		turns = [];
		live = null;
		err = '';
		busy = false;
	}

	// The model writes markdown — lists, bold, the occasional table — and
	// showing it as plain text meant reading literal asterisks and dashes.
	// render() escapes, parses and sanitises, so nothing in an answer can
	// inject markup.
	const html = (text) => render(text || '');

	// Citations are swapped in afterwards, over the rendered DOM, rather than
	// before parsing: [1] inside a fenced code block should stay [1], and a
	// marker smuggled through the parser would not survive sanitising.
	function cites(node, turn) {
		if (!node) return;
		const walk = document.createTreeWalker(node, NodeFilter.SHOW_TEXT);
		const hits = [];
		while (walk.nextNode()) {
			if (/\[\d+\]/.test(walk.currentNode.nodeValue)) hits.push(walk.currentNode);
		}
		for (const t of hits) {
			if (t.parentElement?.closest('pre, code')) continue;
			const frag = document.createDocumentFragment();
			let last = 0;
			const re = /\[(\d+)\]/g;
			let m;
			while ((m = re.exec(t.nodeValue))) {
				if (m.index > last) {
					frag.append(document.createTextNode(t.nodeValue.slice(last, m.index)));
				}
				const n = Number(m[1]);
				const b = document.createElement('button');
				b.className = 'cite';
				b.type = 'button';
				b.textContent = String(n);
				b.addEventListener('click', () => go(turn, n));
				frag.append(b);
				last = m.index + m[0].length;
			}
			if (last < t.nodeValue.length) {
				frag.append(document.createTextNode(t.nodeValue.slice(last)));
			}
			t.replaceWith(frag);
		}
	}

	// Runs after every update, including each streamed delta.
	function answerNode(node, turn) {
		cites(node, turn);
		return {
			update(t) {
				cites(node, t);
			}
		};
	}

	function href(s) {
		if (s.source_type === 'note' && s.slug) return `/note/${s.slug}`;
		if (s.source_type === 'event') return `/calendar?event=${s.source_id}`;
		if (s.source_type === 'message') return `/calendar?mail=${s.source_id}`;
		if (s.source_type === 'task') return `/?task=${s.source_id}`;
		return '/';
	}
	const mark = { note: 'note', message: 'mail', event: 'event', task: 'task' };
	function go(turn, n) {
		const s = turn.sources.find((x) => x.n === n);
		if (s) window.location.href = href(s);
	}
</script>

<form class="quickadd askbar" onsubmit={(e) => (e.preventDefault(), ask())}>
	<input
		bind:value={q}
		placeholder={turns.length ? 'Ask a follow-up…' : 'Ask about your notes, mail and tasks…'}
		enterkeyhint="send"
		aria-label="Ask a question"
	/>
	{#if busy}
		<button type="button" onclick={stop}>Stop</button>
	{:else}
		<button class="primary" type="submit" disabled={!q.trim()}>Ask</button>
	{/if}
</form>

{#if turns.length}
	<div class="row" style="margin:-8px 0 14px">
		<span class="muted" style="font-size:12.5px">
			{turns.length} exchange{turns.length > 1 ? 's' : ''} — follow-ups know the context
		</span>
		<span class="spacer"></span>
		<button class="linkbtn" onclick={reset}>New</button>
	</div>
{/if}

{#if !turns.length && !live}
	<div class="empty">
		<p style="font-size:17px;color:var(--text);margin-bottom:6px">Ask anything</p>
		<span class="muted">
			It answers only from what you actually have, and shows which source each part came
			from. Follow-ups remember the conversation.
		</span>
	</div>
{/if}

{#if err}<div class="banner err">{err}</div>{/if}

{#each [...turns, ...(live ? [live] : [])] as t, i (i)}
	<div class="askq">{t.question}</div>

	{#if t.queries?.length && !t.answer}
		<!-- The wait says what it is doing. A question is often not the query
		     the index can answer, so it looks for several phrasings. -->
		<div class="looking">
			{#each t.queries as qq}<span class="lookchip">{qq}</span>{/each}
		</div>
	{/if}

	{#if !t.answer && !t.sources.length}
		<div class="answer rendered"><span class="caret"></span></div>
	{:else}
		<div class="answer rendered">
			<!-- render() escapes before parsing and sanitises after, so an
			     answer cannot inject markup. -->
			{#key t.answer}
				<div use:answerNode={t}>{@html html(t.answer)}</div>
			{/key}
			{#if busy && t === live}<span class="caret"></span>{/if}
		</div>
	{/if}

	{#if t.sources.length}
		<details class="srcwrap" open={i === turns.length && !turns.length}>
			<summary>{t.sources.length} sources</summary>
			<div class="group marked">
				{#each t.sources as s (s.n)}
					<a class="grow rich" href={href(s)}>
						<span class="gmark {s.source_type}">
							<Icon name={mark[s.source_type] ?? 'note'} size={17} />
						</span>
						<span class="gbody">
							<span class="gmeta"><span>{s.n}</span></span>
							<span class="gtitle">{s.title || '(untitled)'}</span>
							{#if s.snippet}<span class="gtext">{s.snippet}</span>{/if}
						</span>
					</a>
				{/each}
			</div>
		</details>
	{/if}
{/each}
