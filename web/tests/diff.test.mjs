// Bulk conflict resolution. A bug here silently destroys the user's writing,
// which is the worst failure this app has, so the guarantee "merge all loses
// nothing" is pinned down rather than assumed.
import assert from 'node:assert/strict';
import { diffLines, diffStats, mergeBoth } from '../src/lib/diff.js';

let pass = 0, fail = 0;
const t = (name, fn) => {
	try { fn(); pass++; }
	catch (e) { fail++; console.log(`  FAIL  ${name}\n        ${e.message.split('\n').slice(0,3).join('\n        ')}`); }
};

const MINE = 'title\n\nalpha\nbravo\ncharlie\n\nend';
const THEIRS = 'title\n\nalpha\nBRAVO EDITED\ncharlie\ndelta added\n\nend';

t('merge keeps every line from both sides', () => {
	const m = mergeBoth(MINE, THEIRS);
	for (const l of MINE.split('\n')) assert.ok(m.includes(l), `lost mine: ${JSON.stringify(l)}`);
	for (const l of THEIRS.split('\n')) assert.ok(m.includes(l), `lost theirs: ${JSON.stringify(l)}`);
});

t('merging a document with itself changes nothing', () => {
	assert.equal(mergeBoth(MINE, MINE), MINE);
});

t('merging against empty keeps the non-empty side', () => {
	assert.equal(mergeBoth(MINE, ''), MINE);
	assert.equal(mergeBoth('', THEIRS), THEIRS);
});

t('both empty stays empty', () => {
	assert.equal(mergeBoth('', ''), '');
});

t('a competing rewrite keeps both wordings', () => {
	const m = mergeBoth('one\ntwo', 'one\nTWO');
	assert.ok(m.includes('two'), 'dropped mine');
	assert.ok(m.includes('TWO'), 'dropped theirs');
});

t('an insertion lands between its neighbours', () => {
	assert.equal(mergeBoth('one\ntwo', 'one\ninserted\ntwo'), 'one\ninserted\ntwo');
});

t('edits at both ends are both kept', () => {
	const m = mergeBoth('A\nmid\nZ', 'A2\nmid\nZ2');
	for (const l of ['A', 'A2', 'mid', 'Z', 'Z2']) assert.ok(m.includes(l), `lost ${l}`);
});

t('a long unchanged middle is not duplicated', () => {
	const mid = Array.from({ length: 60 }, (_, i) => `line ${i}`).join('\n');
	const m = mergeBoth(`head mine\n${mid}\ntail`, `head theirs\n${mid}\ntail`);
	assert.equal(m.split('line 30').length - 1, 1, 'unchanged line was duplicated');
});

t('merging is repeatable', () => {
	// Resolving in bulk applies one conflict after another, so a second pass
	// over an already-merged document must not keep growing it.
	const once = mergeBoth(MINE, THEIRS);
	assert.equal(mergeBoth(once, once), once);
});

t('trailing blank lines survive', () => {
	assert.equal(mergeBoth('x\n\n', 'x\n\n'), 'x\n\n');
});

t('diffStats counts both directions', () => {
	const s = diffStats(diffLines('a\nb', 'a\nB\nc'));
	assert.equal(s.removed, 1);
	assert.equal(s.added, 2);
});

t('diff of identical text has no changes', () => {
	const s = diffStats(diffLines(MINE, MINE));
	assert.deepEqual(s, { added: 0, removed: 0 });
});

console.log(`\n  diff: ${pass} passed, ${fail} failed\n`);
process.exit(fail ? 1 : 0);
