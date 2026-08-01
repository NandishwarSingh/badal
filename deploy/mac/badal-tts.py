#!/usr/bin/env python3
"""Kokoro text-to-speech, held open.

Loading the model costs seconds; generating a sentence costs a fraction of
one. A process per utterance would pay the loading cost every time and never
be realtime, so this stays resident and reads work from stdin.

Protocol, both directions, framed so a partial read is never mistaken for a
complete message:

    stdin   one JSON object per line
            {"text": "..."}   speak this
            {"cancel": true}  abandon what is in flight

    stdout  [4-byte big-endian length][1-byte type][payload]
            0  PCM      signed 16-bit little-endian, 24kHz mono
            1  done     end of this utterance
            2  error    utf-8 message

The utterance id is deliberately absent: generation is strictly sequential,
so the caller already knows which request the frames belong to, and a cancel
simply means discard until the next done.
"""

import json
import os
import queue
import struct
import sys
import threading

# mlx-audio prints progress to stdout, which is the binary frame channel here.
# The first run of this interleaved "Creating new KokoroPipeline" into the PCM
# and the reader saw it as a malformed frame. So: keep a private duplicate of
# the real stdout, then point fd 1 at stderr, and anything a library prints
# lands harmlessly in the log.
_OUT = os.fdopen(os.dup(1), "wb", buffering=0)
os.dup2(2, 1)
sys.stdout = sys.stderr

MODEL = "prince-canuma/Kokoro-82M"
VOICE = "af_heart"
RATE = 24000

_cancel = threading.Event()


def frame(kind: int, payload: bytes) -> None:
    _OUT.write(struct.pack(">IB", len(payload), kind))
    _OUT.write(payload)


def to_pcm16(samples) -> bytes:
    import numpy as np

    a = np.asarray(samples, dtype=np.float32).reshape(-1)
    # Kokoro returns roughly -1..1; clipping rather than normalising keeps the
    # loudness of consecutive sentences consistent.
    a = np.clip(a, -1.0, 1.0)
    return (a * 32767.0).astype("<i2").tobytes()


def reader(work: "queue.Queue") -> None:
    """Read stdin on its own thread.

    The generation loop used to read stdin itself, which meant a cancel sat
    unread until the sentence it was cancelling had finished -- barge-in
    silenced the phone while this process happily produced eight more seconds
    of audio nobody would hear. Reading has to keep happening while speaking.
    """
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            msg = json.loads(line)
        except Exception:
            continue
        if msg.get("cancel"):
            _cancel.set()
            # Anything already queued was asked for before the interruption.
            try:
                while True:
                    work.get_nowait()
            except queue.Empty:
                pass
            continue
        work.put(msg)
    work.put(None)


def main() -> None:
    from mlx_audio.tts.utils import load_model

    model = load_model(MODEL)
    # One throwaway pass: the first call compiles Metal kernels, and paying
    # that on the first real question would look like a two-second stall.
    try:
        for _ in model.generate(text="Ready.", voice=VOICE, speed=1.0, lang_code="a"):
            break
    except Exception:
        pass
    frame(1, b"ready")

    work: "queue.Queue" = queue.Queue()
    threading.Thread(target=reader, args=(work,), daemon=True).start()

    while True:
        msg = work.get()
        if msg is None:
            return
        text = (msg.get("text") or "").strip()
        if not text:
            frame(1, b"")
            continue

        _cancel.clear()
        try:
            for seg in model.generate(
                text=text, voice=msg.get("voice") or VOICE,
                speed=float(msg.get("speed") or 1.0), lang_code="a",
            ):
                if _cancel.is_set():
                    break
                audio = getattr(seg, "audio", seg)
                pcm = to_pcm16(audio)
                # ~100ms frames so playback can start on the first one rather
                # than waiting for a whole sentence.
                step = RATE // 10 * 2
                for i in range(0, len(pcm), step):
                    if _cancel.is_set():
                        break
                    frame(0, pcm[i : i + step])
        except Exception as e:  # never take the process down
            frame(2, f"{type(e).__name__}: {e}".encode()[:400])
        frame(1, b"")


if __name__ == "__main__":
    main()
