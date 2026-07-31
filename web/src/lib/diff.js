// Line diff for conflict resolution.
//
// Unified (stacked) rather than side-by-side: two columns of text do not fit
// on a phone, and horizontal scrolling to compare two versions of your own
// note is worse than useless.
//
// Standard LCS over lines. Notes are short, so the O(n*m) table is fine and
// the result is easier to reason about than a Myers implementation.

/**
 * @param {string} a  current text (server)
 * @param {string} b  incoming text (the rejected edit)
 * @returns {Array<{type:'same'|'add'|'del', text:string, aIdx:number, bIdx:number}>}
 */
export function diffLines(a, b) {
  const A = (a ?? "").split("\n");
  const B = (b ?? "").split("\n");

  // Trim the common head and tail first. Most conflicts touch one or two
  // lines in a long note, and this keeps the table small.
  let head = 0;
  while (head < A.length && head < B.length && A[head] === B[head]) head++;
  let tail = 0;
  while (
    tail < A.length - head &&
    tail < B.length - head &&
    A[A.length - 1 - tail] === B[B.length - 1 - tail]
  )
    tail++;

  const midA = A.slice(head, A.length - tail);
  const midB = B.slice(head, B.length - tail);

  const n = midA.length,
    m = midB.length;
  const lcs = Array.from({ length: n + 1 }, () => new Uint32Array(m + 1));
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      lcs[i][j] =
        midA[i] === midB[j]
          ? lcs[i + 1][j + 1] + 1
          : Math.max(lcs[i + 1][j], lcs[i][j + 1]);
    }
  }

  const out = [];
  for (let k = 0; k < head; k++)
    out.push({ type: "same", text: A[k], aIdx: k, bIdx: k });

  let i = 0,
    j = 0;
  while (i < n && j < m) {
    if (midA[i] === midB[j]) {
      out.push({ type: "same", text: midA[i], aIdx: head + i, bIdx: head + j });
      i++;
      j++;
    } else if (lcs[i + 1][j] >= lcs[i][j + 1]) {
      out.push({ type: "del", text: midA[i], aIdx: head + i, bIdx: -1 });
      i++;
    } else {
      out.push({ type: "add", text: midB[j], aIdx: -1, bIdx: head + j });
      j++;
    }
  }
  while (i < n)
    out.push({ type: "del", text: midA[i], aIdx: head + i++, bIdx: -1 });
  while (j < m)
    out.push({ type: "add", text: midB[j], aIdx: -1, bIdx: head + j++ });

  for (let k = 0; k < tail; k++) {
    const ai = A.length - tail + k;
    out.push({
      type: "same",
      text: A[ai],
      aIdx: ai,
      bIdx: B.length - tail + k,
    });
  }
  return out;
}

/** Collapse long unchanged stretches so the changes are actually findable. */
export function collapse(rows, context = 2) {
  const keep = new Set();
  rows.forEach((r, i) => {
    if (r.type === "same") return;
    for (
      let k = Math.max(0, i - context);
      k <= Math.min(rows.length - 1, i + context);
      k++
    ) {
      keep.add(k);
    }
  });
  const out = [];
  let skipped = 0;
  rows.forEach((r, i) => {
    if (keep.has(i)) {
      if (skipped) {
        out.push({ type: "gap", count: skipped });
        skipped = 0;
      }
      out.push(r);
    } else {
      skipped++;
    }
  });
  if (skipped) out.push({ type: "gap", count: skipped });
  return out;
}

export function diffStats(rows) {
  let added = 0,
    removed = 0;
  for (const r of rows) {
    if (r.type === "add") added++;
    else if (r.type === "del") removed++;
  }
  return { added, removed };
}

/**
 * Union of both versions: every line from either side, in diff order.
 *
 * This is what "merge all" resolves to. A line only one side has is kept, and
 * a line both sides rewrote keeps both wordings rather than picking one -- the
 * result may need a tidy-up, but nothing written on either device is lost,
 * which is the only guarantee worth making when resolving in bulk.
 */
export function mergeBoth(a, b) {
  return diffLines(a, b)
    .map((r) => r.text)
    .join("\n");
}
