// Images in notes.
//
// Uploads reuse the sync client's chunk protocol rather than adding a second
// upload path: probe what the server already has, PUT what it does not, then
// commit a file record. Paste the same screenshot into three notes and it is
// stored once -- content addressing gives deduplication for free, and the
// server verifies the BLAKE3 itself, so the hash has to be real.
//
// A whole image goes up as a single chunk. FastCDC exists to avoid resending
// most of a large file after a small edit, and images are never edited in
// place; splitting them would cost round trips and save nothing.
//
// Reads go through the API client, not the browser's image loader. A device
// token lives in a header and an <img src> cannot send one, so every picture
// would 401 for anyone who has not also signed in with a passkey.

import { blake3 } from "@noble/hashes/blake3.js";
import { api, getBase, getToken } from "$lib/api.js";

const MAX_BYTES = 8 << 20; // matches the server's per-chunk ceiling

// Longest edge kept. A 6.1" screen at 3x resolves about 1200px across, and
// these are read on a phone far more often than they are zoomed into, so
// 2048 is already generous. A 3MB camera photo lands around 250KB.
const MAX_EDGE = 2048;
const QUALITY = 0.85;
// Below this, re-encoding costs more than it saves.
const SKIP_UNDER = 300 * 1024;

const hex = (bytes) =>
  Array.from(bytes)
    .map((b) => b.toString(16).padStart(2, "0"))
    .join("");

const EXT = {
  "image/png": "png",
  "image/jpeg": "jpg",
  "image/webp": "webp",
  "image/gif": "gif",
  "image/avif": "avif",
  "image/heic": "heic",
  "image/svg+xml": "svg",
};

// Natural size, read before upload and carried in the reference.
//
// Without it a note with three photographs reflows three times as they
// arrive, and every image is preceded by a collapsed line showing its alt
// text. With it the space is reserved exactly, so a skeleton of the right
// shape sits there and nothing moves when the bytes land.
// Resize and re-encode before upload.
//
// The alternative is sending what the camera produced: seconds to upload,
// seconds to come back down through fetch, and a note that cannot paint until
// the last byte of a 3MB JPEG arrives.
async function shrink(file) {
  // Vectors have no pixels to resample, and re-encoding a GIF through a
  // canvas throws away every frame but the first.
  if (file.type === "image/svg+xml" || file.type === "image/gif") return file;

  let bmp;
  try {
    bmp = await createImageBitmap(file);
  } catch {
    return file; // undecodable here; let the server keep the original
  }
  const longest = Math.max(bmp.width, bmp.height);
  const scale = Math.min(1, MAX_EDGE / longest);
  if (scale === 1 && file.size < SKIP_UNDER) {
    bmp.close?.();
    return file;
  }

  const w = Math.max(1, Math.round(bmp.width * scale));
  const h = Math.max(1, Math.round(bmp.height * scale));
  const canvas = document.createElement("canvas");
  canvas.width = w;
  canvas.height = h;
  const ctx = canvas.getContext("2d");
  if (!ctx) {
    bmp.close?.();
    return file;
  }
  ctx.drawImage(bmp, 0, 0, w, h);
  bmp.close?.();

  const blob = await new Promise((res) =>
    canvas.toBlob(res, "image/webp", QUALITY),
  );
  // Safari once refused webp from a canvas; jpeg is the universal fallback.
  const out =
    blob ??
    (await new Promise((res) => canvas.toBlob(res, "image/jpeg", QUALITY)));
  // A screenshot of flat colour can come out larger as webp than as the
  // original png. Keeping whichever is smaller costs one comparison.
  if (!out || out.size >= file.size) return file;

  const base = (file.name || "image").replace(/\.[a-z0-9]+$/i, "");
  const ext = out.type === "image/webp" ? "webp" : "jpg";
  return new File([out], `${base}.${ext}`, { type: out.type });
}

async function dimensions(file) {
  try {
    const bmp = await createImageBitmap(file);
    const d = { w: bmp.width, h: bmp.height };
    bmp.close?.();
    return d.w && d.h ? d : null;
  } catch {
    // SVG and some HEIC variants cannot be decoded this way. The renderer
    // falls back to a generic shape rather than failing.
    return null;
  }
}

export const isImage = (f) =>
  !!f && typeof f.type === "string" && f.type.startsWith("image/");

/**
 * Store an image and return the reference to put in the note.
 * @returns {Promise<{path: string, ref: string, hash: string, size: number}>}
 */
export async function upload(file, { noteSlug = "inbox" } = {}) {
  if (!isImage(file)) throw new Error("Not an image.");
  if (file.size > MAX_BYTES) {
    throw new Error(
      `Too large — ${Math.round(file.size / 1048576)}MB, limit is 8MB.`,
    );
  }
  const buf = new Uint8Array(await file.arrayBuffer());
  const hash = hex(blake3(buf, { dkLen: 32 }));
  const ext = EXT[file.type] || "bin";
  const path = `notes/${noteSlug}/${hash.slice(0, 16)}.${ext}`;

  // Skip the body entirely when the server already holds these bytes.
  const probe = await api("/v1/chunks/probe", {
    method: "POST",
    body: { hashes: [hash] },
  });
  if ((probe.missing || []).includes(hash)) {
    const res = await fetch(`${getBase()}/v1/chunks/${hash}`, {
      method: "PUT",
      credentials: "include",
      headers: {
        Authorization: "Bearer " + getToken(),
        "Content-Type": "application/octet-stream",
      },
      body: buf,
    });
    if (!res.ok) throw new Error(`Upload failed (${res.status}).`);
  }

  await api("/v1/files", {
    method: "POST",
    body: {
      path,
      hash,
      size: file.size,
      chunks: [hash],
      content_type: file.type,
    },
  });
  return { path, ref: `badal:${path}`, hash, size: file.size };
}

// Object URLs are revoked when nothing references them any more, but a note
// re-renders on every keystroke in preview. Caching by path keeps one URL per
// image for the life of the tab instead of leaking one per render.
const urls = new Map();

/** Resolve a stored path to a displayable URL. */
export async function load(path) {
  if (urls.has(path)) return urls.get(path);
  const p = (async () => {
    // A hung request must not leave a skeleton up forever: that is
    // indistinguishable from "still loading", which is exactly the confusion
    // this had. Better an error with a retry.
    const ctl = new AbortController();
    const timer = setTimeout(() => ctl.abort(), 45000);
    let res;
    try {
      res = await fetch(`${getBase()}/v1/blob/${path}`, {
        credentials: "include",
        headers: { Authorization: "Bearer " + getToken() },
        signal: ctl.signal,
      });
    } finally {
      clearTimeout(timer);
    }
    // A cold archive read answers 202 with a restore queued rather than
    // the bytes, so there is nothing to show yet.
    if (res.status === 202) throw new Error("restoring");
    if (!res.ok) throw new Error(String(res.status));
    return URL.createObjectURL(await res.blob());
  })();
  urls.set(path, p);
  // A failed fetch must not be cached forever, or a transient offline blip
  // makes the image permanently broken for this tab.
  p.catch(() => urls.delete(path));
  return p;
}
