// Speaking, from the Mac when it is there and from the browser when it is not.
//
// Audio arrives as raw 24kHz mono PCM and is scheduled straight into an
// AudioContext. No codec, no MediaSource: those buffer, and a buffer is
// exactly what makes an interruption arrive late. Scheduling raw buffers means
// stopping is instant, which is the whole point of barge-in.

import { getBase, getToken } from "./api.js";

const RATE = 24000;

export const speech = $state({
  provider: "browser", // mac | browser
  speaking: false,
  enabled: false, // only after a gesture: iOS will not start audio without one
});

let ws = null;
let ctx = null;
let nextAt = 0;
let sources = [];
let queued = [];

function audio() {
  if (!ctx)
    ctx = new (window.AudioContext || window.webkitAudioContext)({
      sampleRate: RATE,
    });
  if (ctx.state === "suspended") ctx.resume();
  return ctx;
}

/** Must be called from a user gesture, or iOS refuses to make sound at all. */
export async function unlock() {
  speech.enabled = true;
  const a = audio();
  // A silent blip: iOS treats the first buffer as the thing the gesture
  // authorised, and without it the first real sentence is swallowed.
  const b = a.createBuffer(1, 1, RATE);
  const s = a.createBufferSource();
  s.buffer = b;
  s.connect(a.destination);
  s.start();
  await connect();
}

export async function connect() {
  if (ws && ws.readyState <= 1) return;
  try {
    const st = await fetch(`${getBase()}/v1/voice/status`, {
      credentials: "include",
      headers: { Authorization: "Bearer " + getToken() },
    }).then((r) => r.json());
    speech.provider = st.provider || "browser";
  } catch {
    speech.provider = "browser";
  }
  if (speech.provider !== "mac") return;

  // The token cannot ride in a header on a browser WebSocket, so the cookie
  // does it. A device-token-only session falls back to the browser voice.
  const url = getBase().replace(/^http/, "ws") + "/v1/voice/speak";
  try {
    ws = new WebSocket(url);
  } catch {
    speech.provider = "browser";
    return;
  }
  ws.binaryType = "arraybuffer";
  ws.onmessage = onFrame;
  ws.onclose = () => {
    ws = null;
    speech.provider = "browser";
    speech.speaking = false;
  };
  ws.onerror = () => {
    speech.provider = "browser";
  };
  await new Promise((res) => {
    const t = setTimeout(res, 2500);
    ws.onopen = () => {
      clearTimeout(t);
      // Anything said while the socket was still opening.
      queued.splice(0).forEach((t2) => say(t2));
      res();
    };
  });
}

function onFrame(e) {
  if (typeof e.data === "string") {
    try {
      const m = JSON.parse(e.data);
      if (m.provider) speech.provider = m.provider;
    } catch {
      /* not for us */
    }
    return;
  }
  const b = new Uint8Array(e.data);
  const kind = b[0];
  if (kind === 1) {
    // End of an utterance. Speaking stays true until the audio actually
    // finishes playing, which is later than the last frame arriving.
    return;
  }
  if (kind === 2) {
    speech.provider = "browser";
    return;
  }

  const a = audio();
  const pcm = new Int16Array(e.data.slice(1));
  const buf = a.createBuffer(1, pcm.length, RATE);
  const ch = buf.getChannelData(0);
  for (let i = 0; i < pcm.length; i++) ch[i] = pcm[i] / 32768;

  const src = a.createBufferSource();
  src.buffer = buf;
  src.connect(a.destination);
  // Scheduled back to back against the clock rather than played on arrival,
  // so jitter in the network does not become a gap in the sentence.
  const now = a.currentTime;
  if (nextAt < now + 0.05) nextAt = now + 0.05;
  src.start(nextAt);
  nextAt += buf.duration;
  sources.push(src);
  speech.speaking = true;
  src.onended = () => {
    sources = sources.filter((s) => s !== src);
    if (!sources.length) speech.speaking = false;
  };
}

/** Say one sentence. Falls back to the browser voice when the Mac is absent. */
export function say(text) {
  const clean = forSpeech(text);
  if (!clean || !speech.enabled) return;

  if (speech.provider === "mac" && ws?.readyState === 1) {
    ws.send(JSON.stringify({ say: clean }));
    return;
  }
  if (speech.provider === "mac") {
    queued.push(text); // socket still opening
    return;
  }
  browserSay(clean);
}

function browserSay(text) {
  if (typeof speechSynthesis === "undefined") return;
  const u = new SpeechSynthesisUtterance(text);
  u.rate = 1.02;
  const v = speechSynthesis
    .getVoices()
    .find(
      (x) => /en-(GB|US)/.test(x.lang) && /enhanced|premium|siri/i.test(x.name),
    );
  if (v) u.voice = v;
  u.onstart = () => (speech.speaking = true);
  u.onend = () => (speech.speaking = false);
  speechSynthesis.speak(u);
}

/** Stop immediately. This is what barge-in calls. */
export function stopSpeaking() {
  queued = [];
  if (ws?.readyState === 1) ws.send(JSON.stringify({ cancel: true }));
  for (const s of sources) {
    try {
      s.stop();
    } catch {
      /* already ended */
    }
  }
  sources = [];
  nextAt = 0;
  if (typeof speechSynthesis !== "undefined") speechSynthesis.cancel();
  speech.speaking = false;
}

/**
 * What to actually say. The answer is markdown with [1] citations, and read
 * literally that is "asterisk asterisk Schneider" and "bracket one".
 */
export function forSpeech(md) {
  return (md || "")
    .replace(/```[\s\S]*?```/g, " ")
    .replace(/`([^`]*)`/g, "$1")
    .replace(/!\[[^\]]*\]\([^)]*\)/g, " ")
    .replace(/\[([^\]]*)\]\([^)]*\)/g, "$1")
    .replace(/\[\d+\]/g, "") // citation markers
    .replace(/^\s*[#>]+\s*/gm, "")
    .replace(/^\s*[-*+]\s+/gm, "")
    .replace(/[*_~]/g, "")
    .replace(/₹\s?([\d,]+)/g, "$1 rupees")
    .replace(/\s+/g, " ")
    .trim();
}

/**
 * Complete sentences in `text` beyond `from`, and where they end.
 * Speech starts on the first finished sentence rather than the whole answer,
 * which is the difference between talking and reading something out later.
 */
export function sentences(text, from) {
  const rest = text.slice(from);
  const out = [];
  let cut = from;
  const re = /[^.!?\n]+[.!?]+(?=\s|$)|[^\n]+\n/g;
  let m;
  while ((m = re.exec(rest))) {
    const s = m[0].trim();
    if (s.length > 1) out.push(s);
    cut = from + m.index + m[0].length;
  }
  return { out, cut };
}
