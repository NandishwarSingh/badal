// Turning the plain-text half of an email into something readable.
//
// What actually arrives looks like this:
//
//     *Office **of **the **Men*
//   *'**s **H**ostel*
//     This is to inform all the students that the ONLINE MESS CHANGE
//   for the month
//   of August 2026 is scheduled between...
//
// Three separate problems: Gmail's asterisk bold markers landing mid-word,
// hard wrapping at ~76 columns that fights the phone's own wrapping, and
// erratic indentation. Rendering that in a <pre> block, as the first version
// did, shows every one of them.

const esc = (s) =>
	s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');

const BULLET = /^\s*([·•▪◦*\-–—]|\d{1,2}[.)])\s+/;

/**
 * Clean a plain-text mail body into safe HTML.
 * Input is escaped first, so nothing in the mail can inject markup.
 */
export function renderMail(raw) {
	if (!raw) return '';
	let s = esc(String(raw));

	s = s.replace(/\r\n?/g, '\n');
	// Zero-width and non-breaking junk that survives HTML-to-text conversion.
	s = s.replace(/[​-‍﻿]/g, '').replace(/ /g, ' ');

	// Asterisks are stripped rather than converted to <strong>. Gmail breaks
	// them across word boundaries ("**of **the"), so any attempt to pair them
	// produces worse mangling than simply dropping them.
	s = s.replace(/\*{1,3}/g, '');

	// Trailing spaces before newlines, and runs of spaces inside lines.
	s = s
		.split('\n')
		.map((l) => l.replace(/[ \t]+$/, '').replace(/[ \t]{2,}/g, ' '))
		.join('\n');

	// Unwrap hard wrapping. A line is a continuation if the previous one is
	// long, does not end a sentence, and this one does not start a new block.
	const lines = s.split('\n');
	const merged = [];
	for (const line of lines) {
		const t = line.trim();
		const prev = merged.length ? merged[merged.length - 1] : null;
		const isBlock = t === '' || BULLET.test(line) || /^(>|\||=|-{3,}|_{3,})/.test(t);
		// A contraction split across lines ("Men" / "'s Hostel") is always a
		// wrap artifact regardless of how short the previous line was.
		const orphanContraction = /^['’](s|t|re|ve|ll|d|m)\b/i.test(t);
		const prevContinues =
			prev &&
			prev.trim() !== '' &&
			(orphanContraction || prev.trim().length > 45) &&
			!/[.!?:;]$/.test(prev.trim()) &&
			!BULLET.test(prev);
		if (prevContinues && !isBlock) {
			merged[merged.length - 1] = prev.trimEnd() + (orphanContraction ? '' : ' ') + t;
		} else {
			merged.push(line);
		}
	}
	s = merged.join('\n').replace(/\n{3,}/g, '\n\n');

	// Build blocks: bullet runs become lists, everything else paragraphs.
	const out = [];
	let list = [];
	const flushList = () => {
		if (list.length) {
			out.push('<ul>' + list.map((i) => `<li>${i}</li>`).join('') + '</ul>');
			list = [];
		}
	};
	for (const block of s.split('\n\n')) {
		const rows = block.split('\n').filter((r) => r.trim() !== '');
		if (!rows.length) continue;
		if (rows.every((r) => BULLET.test(r))) {
			rows.forEach((r) => list.push(linkify(r.replace(BULLET, '').trim())));
			continue;
		}
		flushList();
		// A short all-caps line on its own is a heading in circular-speak.
		const only = rows.join(' ').trim();
		if (rows.length === 1 && only.length < 60 && only === only.toUpperCase() && /[A-Z]{3}/.test(only)) {
			out.push(`<h4>${linkify(only)}</h4>`);
			continue;
		}
		out.push(`<p>${rows.map((r) => linkify(r.trim())).join('<br>')}</p>`);
	}
	flushList();
	return out.join('');
}

// Runs after escaping, so the URL text is already safe to embed.
function linkify(s) {
	s = s.replace(/\bhttps?:\/\/[^\s<>"')]+/g, (u) => {
		const trimmed = u.replace(/[.,;:]+$/, '');
		const label = trimmed.length > 46 ? trimmed.slice(0, 44) + '…' : trimmed;
		return `<a href="${trimmed}" target="_blank" rel="noopener noreferrer">${label}</a>`;
	});
	s = s.replace(/\b[\w.+-]+@[\w-]+\.[\w.]+\b/g, (m) => `<a href="mailto:${m}">${m}</a>`);
	return s;
}

/** Single-line version for cards and search snippets. */
export function mailSnippet(raw, n = 200) {
	if (!raw) return '';
	const s = String(raw)
		.replace(/\*{1,3}/g, '')
		.replace(/[​-‍﻿]/g, '')
		.replace(/\s+/g, ' ')
		.trim();
	return s.length > n ? s.slice(0, n) + '…' : s;
}
