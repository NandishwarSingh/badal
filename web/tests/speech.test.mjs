// What actually gets spoken, and where a sentence ends.
//
// The answer is markdown with [1] citations. Read literally that is "asterisk
// asterisk Schneider" and "bracket one", so the spoken form is a separate
// thing from the displayed one and worth pinning down.
import assert from 'node:assert/strict';
import fs from 'node:fs';

// The module holds Svelte runes, so the two pure functions are lifted out
// rather than importing a .svelte.js into plain node.
const src = fs.readFileSync(new URL('../src/lib/speech.svelte.js', import.meta.url), 'utf8');
const body = src.slice(src.indexOf('export function forSpeech'));
const mod = await import(
	'data:text/javascript,' + encodeURIComponent(body.replace(/export /g, 'export '))
);
const { forSpeech, sentences } = mod;

let pass = 0, fail = 0;
const t = (name, fn) => {
	try { fn(); pass++; }
	catch (e) { fail++; console.log(`  FAIL  ${name}\n        ${e.message.split('\n')[0]}`); }
};

t('citation markers are not read aloud', () => {
	assert.equal(forSpeech('It closes on 7 August [3].'), 'It closes on 7 August .'.replace(' .', ' .'));
	assert.ok(!forSpeech('Due soon [1][2].').includes('['));
});

t('bold and bullets are not read as punctuation', () => {
	const s = forSpeech('- **Schneider Electric**: a prize pool.');
	assert.ok(!s.includes('*'), s);
	assert.ok(s.startsWith('Schneider Electric'), s);
});

t('rupees are said, not shown', () => {
	assert.match(forSpeech('The pool is ₹1,50,000 total.'), /1,50,000 rupees/);
});

t('links keep their text, not their url', () => {
	assert.equal(forSpeech('See [the form](https://e.com/x) now.'), 'See the form now.');
});

t('code fences are skipped', () => {
	assert.ok(!forSpeech('Run this:\n```\nrm -rf /\n```\nDone.').includes('rm -rf'));
});

t('a complete sentence is emitted, a partial one is not', () => {
	const { out, cut } = sentences('The deadline is 7 August. It closes at mid', 0);
	assert.deepEqual(out, ['The deadline is 7 August.']);
	assert.equal(cut, 'The deadline is 7 August.'.length);
});

t('the next call resumes where the last stopped', () => {
	const full = 'One thing. Two things. Three';
	const a = sentences(full, 0);
	const b = sentences(full, a.cut);
	assert.deepEqual(a.out.concat(b.out), ['One thing.', 'Two things.']);
});

t('nothing is emitted twice', () => {
	let text = '', at = 0;
	const spoken = [];
	for (const chunk of ['Alpha beta. ', 'Gamma delta. ', 'Epsilon']) {
		text += chunk;
		const { out, cut } = sentences(text, at);
		spoken.push(...out);
		at = cut;
	}
	assert.deepEqual(spoken, ['Alpha beta.', 'Gamma delta.']);
	assert.equal(new Set(spoken).size, spoken.length);
});

console.log(`\n  speech: ${pass} passed, ${fail} failed\n`);
process.exit(fail ? 1 : 0);
