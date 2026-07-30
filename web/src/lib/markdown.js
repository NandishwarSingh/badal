// Markdown for notes: parse, sanitise, then enhance.
//
// This replaces a chain of regexes that could not nest a list inside a list,
// had no tables, no blockquotes and no links that were not wiki links. Those
// are not exotic -- they are what writing anything longer than a paragraph
// needs.
//
// Rendering happens in two passes on purpose. render() is synchronous and
// returns HTML immediately, leaving maths and diagrams as empty placeholders.
// enhance() then fills them in, importing KaTeX and Mermaid only if the note
// actually contains any. Mermaid alone is larger than the entire rest of this
// app, and most notes are prose -- making every note pay for it would be a
// slow first paint in exchange for nothing.

import { marked } from "marked";
import DOMPurify from "dompurify";

export const slugify = (s) =>
  String(s)
    .toLowerCase()
    .trim()
    .replace(/[^a-z0-9\s-]/g, "")
    .replace(/\s+/g, "-")
    .replace(/-+/g, "-");

const escapeHtml = (s) =>
  String(s)
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");

// ------------------------------------------------------------- extensions

const wikiLink = {
  name: "wikiLink",
  level: "inline",
  start(src) {
    return src.indexOf("[[");
  },
  tokenizer(src) {
    const m = /^\[\[([^\]|]+)(?:\|([^\]]*))?\]\]/.exec(src);
    if (!m) return;
    return {
      type: "wikiLink",
      raw: m[0],
      target: m[1].trim(),
      label: (m[2] || m[1]).trim(),
    };
  },
  renderer(t) {
    const s = slugify(t.target);
    return `<a class="wiki" data-slug="${escapeHtml(s)}" href="/note/${escapeHtml(s)}">${escapeHtml(t.label)}</a>`;
  },
};

const mathBlock = {
  name: "mathBlock",
  level: "block",
  start(src) {
    return src.indexOf("$$");
  },
  tokenizer(src) {
    const m = /^\$\$([\s\S]+?)\$\$(?:\n+|$)/.exec(src);
    if (!m) return;
    return { type: "mathBlock", raw: m[0], text: m[1].trim() };
  },
  renderer(t) {
    return `<div class="math math-block" data-tex="${escapeHtml(t.text)}"></div>`;
  },
};

const mathInline = {
  name: "mathInline",
  level: "inline",
  start(src) {
    return src.indexOf("$");
  },
  tokenizer(src) {
    // Three guards, all there to keep prices out of the maths renderer:
    // no space just inside either delimiter, and no digit immediately
    // after the closing one. Without the last rule "$5 and $10" parses as
    // maths reading "5 and ".
    const m = /^\$([^\s$][^$\n]*?[^\s$]|[^\s$])\$(?!\d)/.exec(src);
    if (!m) return;
    return { type: "mathInline", raw: m[0], text: m[1] };
  },
  renderer(t) {
    return `<span class="math math-inline" data-tex="${escapeHtml(t.text)}"></span>`;
  },
};

// Fenced blocks. mermaid becomes a diagram placeholder; everything else is
// code, with the language kept so it can be labelled and highlighted later.
const renderer = {
  code(token) {
    const text = token.text ?? "";
    const lang = (token.lang || "").trim().split(/\s+/)[0];
    if (lang === "mermaid") {
      // The source goes in the element's text, not an attribute. DOMPurify
      // strips any attribute value containing "-->" as a comment-injection
      // defence, and "-->" is a flowchart arrow -- every diagram with an edge
      // came back blank.
      return `<div class="mermaid-wrap"><pre class="mermaid-src">${escapeHtml(text)}</pre></div>`;
    }
    const cls = lang ? ` class="language-${escapeHtml(lang)}"` : "";
    const label = lang
      ? `<span class="codelang">${escapeHtml(lang)}</span>`
      : "";
    return `<div class="codeblock">${label}<pre><code${cls}>${escapeHtml(text)}</code></pre></div>`;
  },
  // Tables overflow on a phone. Wrapping each one in its own scroll
  // container is what stops a wide table scrolling the whole page sideways.
  table(token) {
    const cell = (c, tag) => {
      const align = c.align ? ` style="text-align:${c.align}"` : "";
      return `<${tag}${align}>${this.parser.parseInline(c.tokens)}</${tag}>`;
    };
    const head = token.header.map((c) => cell(c, "th")).join("");
    const body = token.rows
      .map((row) => `<tr>${row.map((c) => cell(c, "td")).join("")}</tr>`)
      .join("");
    return `<div class="tablewrap"><table><thead><tr>${head}</tr></thead><tbody>${body}</tbody></table></div>`;
  },
  image(token) {
    const href = token.href || "";
    const alt = escapeHtml(token.text || "");
    const title = token.title ? ` title="${escapeHtml(token.title)}"` : "";
    // Attachments are fetched through the API client rather than by the
    // browser, because a device-token sign-in has no cookie for <img> to
    // send and every picture would 401.
    if (href.startsWith("badal:")) {
      return `<img class="att" data-badal="${escapeHtml(href.slice(6))}" alt="${alt}"${title} loading="lazy">`;
    }
    return `<img src="${escapeHtml(href)}" alt="${alt}"${title} loading="lazy" referrerpolicy="no-referrer">`;
  },
};

marked.use({
  gfm: true,
  breaks: true,
  extensions: [wikiLink, mathBlock, mathInline],
  renderer,
});

const PURIFY = {
  ADD_ATTR: [
    "data-slug",
    "data-tex",
    "data-badal",
    "target",
    "rel",
    "loading",
    "referrerpolicy",
  ],
  ADD_TAGS: ["input"],
  FORBID_TAGS: ["style", "form"],
  FORBID_ATTR: ["onerror", "onload", "onclick"],
};

/**
 * Markdown to HTML, before sanitising. Exported so the parsing rules can be
 * tested in node, where DOMPurify has no DOM to work with.
 */
export function parse(src) {
  if (!src) return "";
  try {
    return marked.parse(String(src));
  } catch {
    // A parser crash must not blank the note. Show the source instead.
    return `<pre class="rawfallback">${escapeHtml(src)}</pre>`;
  }
}

/** Parse markdown to sanitised HTML. Maths and diagrams are left as stubs. */
export function render(src) {
  if (!src) return "";
  return DOMPurify.sanitize(parse(src), PURIFY);
}

// ---------------------------------------------------------------- enhance

let katexLoaded = null;
let mermaidLoaded = null;

async function getKatex() {
  if (!katexLoaded) {
    katexLoaded = (async () => {
      const [mod] = await Promise.all([
        import("katex"),
        import("katex/dist/katex.min.css"),
      ]);
      return mod.default ?? mod;
    })();
  }
  return katexLoaded;
}

async function getMermaid() {
  if (!mermaidLoaded) {
    mermaidLoaded = (async () => {
      const mod = await import("mermaid");
      const m = mod.default ?? mod;
      m.initialize({
        startOnLoad: false,
        securityLevel: "strict",
        theme: "base",
        fontFamily: "inherit",
        // Matched to the app's tokens rather than mermaid's dark theme,
        // which ships a near-black that is not this near-black.
        themeVariables: {
          background: "#000000",
          primaryColor: "#14161a",
          primaryTextColor: "#f1f3f7",
          primaryBorderColor: "rgba(255,255,255,0.18)",
          secondaryColor: "#20232a",
          tertiaryColor: "#0d0e11",
          lineColor: "#5b626d",
          textColor: "#f1f3f7",
          mainBkg: "#14161a",
          nodeBorder: "rgba(255,255,255,0.18)",
          clusterBkg: "#0d0e11",
          clusterBorder: "rgba(255,255,255,0.10)",
          edgeLabelBackground: "#0d0e11",
        },
      });
      return m;
    })();
  }
  return mermaidLoaded;
}

let seq = 0;

/**
 * Fill in the placeholders left by render().
 *
 * Every stage is independently guarded: a note with one broken formula and one
 * good diagram should show the diagram, and a syntax error while typing must
 * never take the page down.
 */
export async function enhance(root, { loadImage } = {}) {
  if (!root) return;

  const maths = root.querySelectorAll(".math[data-tex]:not([data-done])");
  if (maths.length) {
    try {
      const katex = await getKatex();
      for (const el of maths) {
        el.dataset.done = "1";
        try {
          katex.render(el.dataset.tex, el, {
            displayMode: el.classList.contains("math-block"),
            throwOnError: false,
            output: "html",
          });
        } catch (e) {
          el.textContent = el.dataset.tex;
          el.classList.add("math-err");
        }
      }
    } catch {
      for (const el of maths) el.textContent = el.dataset.tex;
    }
  }

  const diagrams = root.querySelectorAll(".mermaid-src:not([data-done])");
  if (diagrams.length) {
    try {
      const mermaid = await getMermaid();
      for (const el of diagrams) {
        const src = el.textContent || "";
        el.dataset.done = "1";
        try {
          const { svg } = await mermaid.render(`mmd${++seq}`, src);
          el.innerHTML = DOMPurify.sanitize(svg, {
            USE_PROFILES: { svg: true, svgFilters: true },
          });
        } catch (e) {
          // Half-typed diagrams are the normal case in a live preview,
          // so this is a quiet note rather than a red error.
          el.className = "mermaid-src mermaid-pending";
        }
      }
    } catch {
      /* leave the source visible as text */
    }
  }

  if (loadImage) {
    for (const img of root.querySelectorAll(
      "img.att[data-badal]:not([data-done])",
    )) {
      img.dataset.done = "1";
      try {
        const url = await loadImage(img.dataset.badal);
        if (url) img.src = url;
        else img.replaceWith(missing(img.alt));
      } catch {
        img.replaceWith(missing(img.alt));
      }
    }
  }
}

function missing(alt) {
  const d = document.createElement("div");
  d.className = "att-missing";
  d.textContent = alt ? `Image unavailable — ${alt}` : "Image unavailable";
  return d;
}

/** True if the source needs the heavy renderers, for deciding to show a spinner. */
export const needsEnhance = (src) =>
  !!src &&
  (/\$\$?[^$]/.test(src) ||
    /```\s*mermaid/.test(src) ||
    /!\[[^\]]*\]\(badal:/.test(src));
