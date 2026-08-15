// Checks that note markdown renders to the HTML the styles expect, and that
// nothing hostile survives sanitising. Run with: pnpm check:render
//
// This exists because two real bugs got through review by inspection. marked
// emits task-list attributes in an order you would not guess, and DOMPurify
// silently strips any attribute containing "-->" -- which is the arrow in
// every flowchart, so diagrams rendered as empty boxes and nothing errored.

const SAMPLE = "Everything the note renderer can do, on one page.\n\n## Text\n\n**Bold**, *italic*, ~~struck through~~, `inline code`, and a [real link](https://pgvector.org).\nLink another note with [[Badal Roadmap]].\n\n> A blockquote, for when something is worth setting apart.\n\n## Lists\n\n- Top level\n  - Nested one level\n    - And two\n- Back to the top\n\n1. Numbered\n2. Keeps counting\n3. Even after a wrap\n\n- [x] Task that is done\n- [ ] Task that is not\n\n## A table\n\n| Store | Holds | Why it is there |\n| --- | --- | --- |\n| Postgres | system of record | everything durable |\n| pgvector | embeddings | retrieval |\n| Garage | blobs | images and mail |\n| SurrealDB | derived graph | links between notes |\n\n## Maths\n\nInline: the golden ratio $\\varphi = \\frac{1+\\sqrt{5}}{2}$ shows up in the layout grid.\nPrices stay prices: lunch was $5 and coffee $10, not maths.\n\nCosine similarity, which is what the search actually computes:\n\n$$\n\\operatorname{sim}(q,d) \\;=\\; \\frac{q \\cdot d}{\\lVert q \\rVert \\, \\lVert d \\rVert}\n\\;=\\; \\frac{\\sum_{i=1}^{n} q_i d_i}{\\sqrt{\\sum q_i^2}\\;\\sqrt{\\sum d_i^2}}\n$$\n\n## A flowchart\n\n```mermaid\nflowchart TD\n  A[New mail arrives] --> B[Parse MIME]\n  B --> C{Has images?}\n  C -->|yes| D[OCR on the Mac]\n  C -->|no| E[Chunk and embed]\n  D --> E\n  E --> F[Tag with subjects]\n  F --> G[(Postgres + pgvector)]\n```\n\n## Code\n\n```go\nfunc fanout(limit int) int {\n\tn := limit * knnFanout\n\tif n < minFanout {\n\t\tn = minFanout\n\t}\n\treturn n\n}\n```\n\n## An image\n\n![Test gradient](badal:notes/markdown-tour/fe480c7ae3a81106.png)\n\n---\n\nIf every section above renders, the pipeline works end to end.\n";

import { JSDOM } from 'jsdom';
const dom = new JSDOM('<!doctype html><body></body>');
globalThis.window = dom.window;
globalThis.document = dom.window.document;
globalThis.Node = dom.window.Node;
globalThis.DocumentFragment = dom.window.DocumentFragment;

const { render } = await import('../src/lib/markdown.js');
const body = SAMPLE;
const html = render(body);

const checks = [
  ['heading',       /<h2[^>]*>Text<\/h2>/],
  ['bold',          /<strong>Bold<\/strong>/],
  ['strike',        /<del>struck through<\/del>/],
  ['external link', /<a href="https:\/\/pgvector\.org"/],
  ['wiki link',     /<a class="wiki" data-slug="badal-roadmap"/],
  ['blockquote',    /<blockquote>/],
  ['nested list',   /<li>Nested one level<ul>|<ul>\s*<li>And two/],
  ['task checked',  /<input checked[^>]*type="checkbox"/],
  ['task unchecked',/<input disabled=""\s+type="checkbox">\s*Task that is not/],
  ['table wrapper', /<div class="tablewrap"><table>/],
  ['table header',  /<th[^>]*>Store<\/th>/],
  // The TeX rides in data-tex and KaTeX typesets it in the browser, so the
  // element itself is empty here. Asserting on its text content was checking
  // for a shape the renderer stopped emitting.
  ['inline math',   /<span class="math math-inline" data-tex="[^"]*varphi/],
  ['block math',    /<div class="math math-block" data-tex="[^"]*operatorname/],
  ['prices intact', /\$5 and coffee \$10/],
  ['mermaid',       /<pre class="mermaid-src">flowchart TD/],
  ['mermaid arrow', /A\[New mail arrives\] --&gt; B/],
  ['code lang',     /<span class="codelang">go<\/span>/],
  ['code escaped',  /n &lt; minFanout|&lt;/],
  ['attachment',    /<img class="att" data-badal="notes\/markdown-tour\//],
  ['hr',            /<hr>/],
];
let bad = 0;
for (const [name, re] of checks) {
  const ok = re.test(html);
  if (!ok) bad++;
  console.log(`  ${ok ? 'ok  ' : 'FAIL'}  ${name}`);
}

// Injection must not survive.
const evil = render('<img src=x onerror=alert(1)>\n\n<script>alert(2)<\/script>\n\n[x](javascript:alert(3))');
for (const [name, badRe] of [['onerror stripped', /onerror/], ['script stripped', /<script/], ['js: href stripped', /javascript:/]]) {
  const ok = !badRe.test(evil);
  if (!ok) bad++;
  console.log(`  ${ok ? 'ok  ' : 'FAIL'}  ${name}`);
}
console.log(bad ? `\n  ${bad} FAILING` : '\n  all pass');
