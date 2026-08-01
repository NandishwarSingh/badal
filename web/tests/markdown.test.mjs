// Parsing rules for note markdown. These run in node against parse(), which is
// everything except the DOMPurify pass -- the custom tokenizers are where the
// risk is, and they need no DOM.
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';

// render() sanitises with DOMPurify, which needs a window. Standing one up
// here means the XSS boundary is actually tested rather than assumed -- notes
// can be built from parsed mail and OCR output, so the input is not all typed
// by hand.
const dom = new JSDOM('');
globalThis.window = dom.window;
globalThis.document = dom.window.document;
globalThis.Node = dom.window.Node;
globalThis.DocumentFragment = dom.window.DocumentFragment;

const { parse, render } = await import('../src/lib/markdown.js');

let pass = 0, fail = 0;
const t = (name, fn) => {
	try { fn(); pass++; }
	catch (e) { fail++; console.log(`  FAIL  ${name}\n        ${e.message.split('\n')[0]}`); }
};

t('inline maths renders as a placeholder', () => {
	const h = parse('Euler: $e^{i\\pi}+1=0$ done');
	assert.match(h, /class="math math-inline" data-tex="e\^\{i\\pi\}\+1=0"/);
});

t('block maths renders as a placeholder', () => {
	const h = parse('$$\n\\int_0^1 x\\,dx\n$$');
	assert.match(h, /class="math math-block"/);
	assert.match(h, /data-tex="\\int_0\^1 x\\,dx"/);
});

// The reason the closing delimiter must not be followed by a digit.
t('prices are not maths', () => {
	const h = parse('It costs $5 and $10 total.');
	assert.doesNotMatch(h, /math-inline/, 'a price was parsed as maths');
});

t('a lone dollar is not maths', () => {
	assert.doesNotMatch(parse('Cost in $ please'), /math-inline/);
});

t('maths needs no space inside the delimiters', () => {
	assert.doesNotMatch(parse('from $ x $ to y'), /math-inline/);
});

t('mermaid fence becomes a diagram, not code', () => {
	const h = parse('```mermaid\nflowchart TD\n  A --> B\n```');
	assert.match(h, /class="mermaid-src">flowchart TD/);
	assert.doesNotMatch(h, /<code/);
});

t('other fences stay code and keep their language', () => {
	const h = parse('```go\nfunc main() {}\n```');
	assert.match(h, /class="language-go"/);
	assert.match(h, /codelang">go</);
	assert.match(h, /func main\(\) \{\}/);
});

t('wiki links resolve to a slug', () => {
	const h = parse('See [[My Great Note]] here');
	assert.match(h, /class="wiki" data-slug="my-great-note" href="\/note\/my-great-note"/);
});

t('wiki links support a label', () => {
	const h = parse('See [[target page|the label]]');
	assert.match(h, /data-slug="target-page"/);
	assert.match(h, />the label</);
});

t('tables get their own scroll container', () => {
	const h = parse('| a | b |\n| --- | --- |\n| 1 | 2 |');
	assert.match(h, /<div class="tablewrap"><table>/);
	assert.match(h, /<th>a<\/th>/);
});

t('nested lists nest', () => {
	const h = parse('- one\n  - inner\n- two');
	assert.match(h, /<ul>[\s\S]*<ul>/, 'inner list was flattened');
});

t('task lists produce checkboxes', () => {
	const h = parse('- [ ] todo\n- [x] done');
	assert.match(h, /type="checkbox"/);
});

t('blockquotes work', () => {
	assert.match(parse('> quoted'), /<blockquote>/);
});

t('badal images defer to the API loader', () => {
	const h = parse('![shot](badal:notes/x/abc.png)');
	assert.match(h, /class="att" data-badal="notes\/x\/abc\.png"/);
	assert.doesNotMatch(h, /src="badal:/, 'browser would try to fetch badal: directly');
});

// Reading a blob through fetch means nothing paints until the last byte
// lands. On a 3MB photo that was seconds of an <img> with no src, which the
// browser renders as its alt text -- the space has to be reserved up front.
t('a sized image reserves its exact ratio', () => {
	const h = render('![shot](badal:notes/a/b.png#1600x900)');
	assert.match(h, /class="shot"/);
	assert.match(h, /aspect-ratio:1600 \/ 900/);
	assert.match(h, /data-badal="notes\/a\/b\.png"/, 'the fragment must not stay in the path');
});

t('an unsized image gets a guess it can correct later', () => {
	const h = render('![image](badal:notes/a/c.jpg)');
	assert.match(h, /class="shot guess"/);
	assert.match(h, /aspect-ratio:16 \/ 10/);
});

t('external images keep their src', () => {
	assert.match(parse('![x](https://e.com/a.png)'), /src="https:\/\/e\.com\/a\.png"/);
});

// parse() deliberately passes raw HTML through; render() is the boundary.
t('render strips inline event handlers', () => {
	const h = render('<img src=x onerror="alert(1)">');
	assert.doesNotMatch(h, /onerror/, 'inline handler survived sanitising');
});

t('render strips script tags', () => {
	const h = render('hello <script>alert(1)</script> world');
	assert.doesNotMatch(h, /<script/i);
	assert.match(h, /hello/);
});

t('render strips javascript: links', () => {
	const h = render('[click](javascript:alert(1))');
	assert.doesNotMatch(h, /javascript:/i);
});

t('render keeps the placeholders enhance() needs', () => {
	const h = render('$x^2$ and [[a note]] and ![i](badal:notes/a/b.png)');
	assert.match(h, /data-tex="x\^2"/, 'maths placeholder was stripped');
	assert.match(h, /data-slug="a-note"/, 'wiki link data attribute was stripped');
	assert.match(h, /data-badal="notes\/a\/b\.png"/, 'image reference was stripped');
});

// Arrows are the case that broke: DOMPurify drops attribute values holding
// "-->", so the source lives in text content instead.
t('render keeps mermaid source including arrows', () => {
	const h = render('```mermaid\nflowchart TD\n  A-->B\n```');
	assert.match(h, /class="mermaid-src"/);
	assert.match(h, /flowchart TD/);
	assert.match(h, /A--&gt;B/, 'the arrow was stripped');
});

t('render keeps task list checkboxes', () => {
	assert.match(render('- [x] done'), /type="checkbox"/);
});

t('empty input is empty', () => {
	assert.equal(parse(''), '');
	assert.equal(parse(null), '');
});

console.log(`\n  markdown: ${pass} passed, ${fail} failed\n`);
process.exit(fail ? 1 : 0);
