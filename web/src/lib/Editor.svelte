<script>
	import { upload, isImage } from '$lib/attach.js';
	import { toast } from '$lib/toast.svelte.js';

	let { value = $bindable(''), slug = 'inbox', oninput } = $props();

	let ta = $state(null);
	let uploading = $state(0);

  // The capture screen focuses the body after the title, and bind:this on a
  // component yields the instance rather than the textarea inside it.
  export function focus() {
    ta?.focus();
  }

  let help = $state(false);
  let kbInset = $state(0);

  // On a phone the software keyboard covers the bottom of the layout but the
  // page does not shrink, so a bottom-docked toolbar ends up underneath it.
  // visualViewport reports how much is actually covered; lifting the bar by
  // that much is the only way it stays reachable while typing.
  $effect(() => {
    const vv = typeof window !== "undefined" && window.visualViewport;
    if (!vv) return;
    const update = () => {
      const covered = window.innerHeight - vv.height - vv.offsetTop;
      kbInset = covered > 90 ? covered : 0;
    };
    update();
    vv.addEventListener("resize", update);
    vv.addEventListener("scroll", update);
    return () => {
      vv.removeEventListener("resize", update);
      vv.removeEventListener("scroll", update);
    };
  });

  const words = $derived(
    (value || "").trim() ? (value || "").trim().split(/\s+/).length : 0,
  );
  // 200 wpm is the usual reading-speed figure and is close enough to be
  // useful for deciding whether a note is a paragraph or an essay.
  const mins = $derived(Math.max(1, Math.round(words / 200)));

	// Every action is a pure transform of (text, selection), so the same code
	// serves the toolbar buttons and the keyboard shortcuts.
	const WRAPS = {
		bold: ['**', '**'],
		italic: ['*', '*'],
		strike: ['~~', '~~'],
		code: ['`', '`'],
		math: ['$', '$']
	};

	function apply(fn) {
		if (!ta) return;
		const res = fn(value, ta.selectionStart, ta.selectionEnd);
		value = res.text;
		oninput?.();
		// Restore the caret after Svelte writes the value back, or the browser
		// drops it at the end and typing continues in the wrong place.
		requestAnimationFrame(() => {
			ta.focus();
			ta.setSelectionRange(res.start, res.end ?? res.start);
		});
	}

	const wrap = (kind) => () =>
		apply((text, s, e) => {
			const [open, close] = WRAPS[kind];
			const sel = text.slice(s, e);
			// Toggle off when the selection is already wrapped.
			if (text.slice(Math.max(0, s - open.length), s) === open && text.slice(e, e + close.length) === close) {
				return {
					text: text.slice(0, s - open.length) + sel + text.slice(e + close.length),
					start: s - open.length,
					end: e - open.length
				};
			}
			return {
				text: text.slice(0, s) + open + sel + close + text.slice(e),
				start: s + open.length,
				end: e + open.length
			};
		});

	// Line prefixes: headings, quotes, lists. Applied to every line the
	// selection touches, and removed again if all of them already have it.
	const prefix = (mark, { numbered = false } = {}) =>
		() =>
			apply((text, s, e) => {
				const ls = text.lastIndexOf('\n', s - 1) + 1;
				let le = text.indexOf('\n', e);
				if (le === -1) le = text.length;
				const lines = text.slice(ls, le).split('\n');
				const re = numbered ? /^\d+\.\s/ : new RegExp('^' + mark.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'));
				const allHave = lines.every((l) => re.test(l));
				const out = lines
					.map((l, i) => (allHave ? l.replace(re, '') : (numbered ? `${i + 1}. ` : mark) + l))
					.join('\n');
				return { text: text.slice(0, ls) + out + text.slice(le), start: ls, end: ls + out.length };
			});

	const insertBlock = (snippet) => () =>
		apply((text, s, e) => {
			const pad = s > 0 && text[s - 1] !== '\n' ? '\n\n' : '';
			const body = pad + snippet + '\n';
			return { text: text.slice(0, s) + body + text.slice(e), start: s + body.length };
		});

	const link = () =>
		apply((text, s, e) => {
			const sel = text.slice(s, e) || 'text';
			const out = `[${sel}](url)`;
			// Leave "url" selected so the next keystroke replaces it.
			return {
				text: text.slice(0, s) + out + text.slice(e),
				start: s + sel.length + 3,
				end: s + sel.length + 6
			};
		});

	const wikiLink = () =>
		apply((text, s, e) => {
			const sel = text.slice(s, e) || 'note name';
			return {
				text: text.slice(0, s) + `[[${sel}]]` + text.slice(e),
				start: s + 2,
				end: s + 2 + sel.length
			};
		});

	// -------------------------------------------------------------- images

	async function addFiles(files) {
		const imgs = [...files].filter(isImage);
		if (!imgs.length) return;
		uploading += imgs.length;
		for (const f of imgs) {
			try {
				const { ref } = await upload(f, { noteSlug: slug });
				const alt = (f.name || 'image').replace(/\.[a-z0-9]+$/i, '').slice(0, 60);
				apply((text, s, e) => {
					const pad = s > 0 && text[s - 1] !== '\n' ? '\n\n' : '';
					const md = `${pad}![${alt}](${ref})\n`;
					return { text: text.slice(0, s) + md + text.slice(e), start: s + md.length };
				});
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

	let picker = $state(null);

	// -------------------------------------------------------------- typing

	function onKeydown(e) {
		const mod = e.metaKey || e.ctrlKey;
		if (mod && !e.altKey) {
			const k = e.key.toLowerCase();
			const map = { b: 'bold', i: 'italic', e: 'code' };
			if (map[k]) {
				e.preventDefault();
				wrap(map[k])();
				return;
			}
			if (k === 'k') {
				e.preventDefault();
				link();
				return;
			}
		}

		// Tab indents instead of leaving the field, which is what is expected
		// inside a textarea. Escape then Tab is the way out.
		if (e.key === 'Tab') {
			e.preventDefault();
			apply((text, s, en) => {
				if (s !== en) {
					const ls = text.lastIndexOf('\n', s - 1) + 1;
					let le = text.indexOf('\n', en);
					if (le === -1) le = text.length;
					const out = text
						.slice(ls, le)
						.split('\n')
						.map((l) => (e.shiftKey ? l.replace(/^ {1,2}/, '') : '  ' + l))
						.join('\n');
					return { text: text.slice(0, ls) + out + text.slice(le), start: ls, end: ls + out.length };
				}
				if (e.shiftKey) {
					const ls = text.lastIndexOf('\n', s - 1) + 1;
					if (text.slice(ls, ls + 2) === '  ') {
						return { text: text.slice(0, ls) + text.slice(ls + 2), start: Math.max(ls, s - 2) };
					}
					return { text, start: s };
				}
				return { text: text.slice(0, s) + '  ' + text.slice(en), start: s + 2 };
			});
			return;
		}

		// Continue the current list. Enter on an empty item ends the list
		// rather than adding another empty one.
		if (e.key === 'Enter' && !e.shiftKey && !mod) {
			const s = ta.selectionStart;
			if (s !== ta.selectionEnd) return;
			const ls = value.lastIndexOf('\n', s - 1) + 1;
			const line = value.slice(ls, s);
			const m = /^(\s*)(?:([-*+])\s+(\[[ xX]\]\s+)?|(\d+)\.\s+)/.exec(line);
			if (!m) return;
			e.preventDefault();
			if (line.slice(m[0].length).trim() === '') {
				apply((text) => ({ text: text.slice(0, ls) + text.slice(s), start: ls }));
				return;
			}
			const indent = m[1] || '';
			const next = m[4] ? `${indent}${Number(m[4]) + 1}. ` : `${indent}${m[2]} ${m[3] ? '[ ] ' : ''}`;
			apply((text) => ({
				text: text.slice(0, s) + '\n' + next + text.slice(s),
				start: s + 1 + next.length
			}));
		}
	}
</script>

<!-- Scrolls horizontally on a phone, wraps on a desktop. Same reasoning as the
     subject chips: a touch row needs a scroll affordance, a mouse row does not. -->
<div
  class="mdtoolbar"
  role="toolbar"
  aria-label="Formatting"
  style={kbInset ? `bottom:${kbInset}px` : ""}
  class:lifted={!!kbInset}
>
	<button title="Bold  ⌘B" onclick={wrap('bold')} aria-label="Bold"><b>B</b></button>
	<button title="Italic  ⌘I" onclick={wrap('italic')} aria-label="Italic"><i>I</i></button>
	<button title="Strikethrough" onclick={wrap('strike')} aria-label="Strikethrough"><s>S</s></button>
	<span class="tsep"></span>
	<button title="Heading" onclick={prefix('## ')} aria-label="Heading">H</button>
	<button title="Bullet list" onclick={prefix('- ')} aria-label="Bullet list">•</button>
	<button title="Numbered list" onclick={prefix('', { numbered: true })} aria-label="Numbered list">1.</button>
	<button title="Task" onclick={prefix('- [ ] ')} aria-label="Task list">☐</button>
	<button title="Quote" onclick={prefix('> ')} aria-label="Quote">❝</button>
	<span class="tsep"></span>
	<button title="Link  ⌘K" onclick={link} aria-label="Link">↗</button>
	<button title="Link a note" onclick={wikiLink} aria-label="Link a note">[[]]</button>
	<button title="Inline code  ⌘E" onclick={wrap('code')} aria-label="Inline code">&lt;/&gt;</button>
	<button title="Code block" onclick={insertBlock('```\n\n```')} aria-label="Code block">▤</button>
	<span class="tsep"></span>
	<button title="Inline maths" onclick={wrap('math')} aria-label="Inline maths">𝑥</button>
	<button title="Maths block" onclick={insertBlock('$$\n\n$$')} aria-label="Maths block">∑</button>
	<button
		title="Flowchart"
		aria-label="Flowchart"
		onclick={insertBlock(
			'```mermaid\nflowchart TD\n  A[Start] --> B{Choice}\n  B -->|yes| C[Do it]\n  B -->|no| D[Skip]\n```'
		)}>⤳</button
	>
	<button title="Table" onclick={insertBlock('| Column | Column |\n| --- | --- |\n|  |  |')} aria-label="Table">▦</button>
	<span class="tsep"></span>
	<button title="Image" onclick={() => picker?.click()} aria-label="Insert image">
		{#if uploading}<span class="spinner"></span>{:else}▣{/if}
	</button>
	<span class="tsep"></span>
	<button
		title="Markdown help"
		class:on={help}
		aria-pressed={help}
		onclick={() => (help = !help)}
		aria-label="Markdown help">?</button
	>
</div>

{#if help}
	<div class="cheat">
		<div><code>**bold**</code> <code>*italic*</code> <code>~~strike~~</code></div>
		<div><code># Heading</code> <code>&gt; quote</code> <code>---</code></div>
		<div><code>- item</code> <code>1. item</code> <code>- [ ] task</code></div>
		<div><code>`code`</code> <code>```go</code> fenced block</div>
		<div><code>[text](url)</code> <code>[[Another note]]</code></div>
		<div><code>| a | b |</code> then <code>| --- | --- |</code></div>
		<div><code>$x^2$</code> maths, <code>$$…$$</code> block</div>
		<div><code>```mermaid</code> flowchart, sequence, pie…</div>
		<div class="muted">Two spaces indent a nested list. Paste or drop an image to upload it.</div>
	</div>
{/if}

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

<textarea
	bind:this={ta}
	class="editor-body"
	bind:value
	oninput={() => oninput?.()}
	onkeydown={onKeydown}
	onpaste={onPaste}
	ondrop={onDrop}
	spellcheck="true"
	placeholder={'Write in markdown.\n\n# Heading   **bold**   - list   > quote\n[[Link another note]]\n$e^{i\\pi}+1=0$ for maths\nA ```mermaid block for a flowchart\nPaste or drop an image straight in'}
></textarea>

<div class="edstatus">
	{#if uploading}
		<span class="upl">
			<span class="spinner"></span> Uploading {uploading} image{uploading > 1 ? 's' : ''}…
		</span>
	{:else}
		<span>{words} word{words === 1 ? '' : 's'}</span>
		{#if words > 60}<span>· {mins} min read</span>{/if}
	{/if}
	<span class="spacer"></span>
	<span class="kbdhint">⌘B ⌘I ⌘K · Tab indents</span>
</div>
