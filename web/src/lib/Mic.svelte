<script>
	// Hold to speak.
	//
	// Hold rather than tap-to-start-tap-to-stop: you always know whether it is
	// listening, because your finger is on it. Releasing is the same gesture
	// as finishing the sentence, and there is no state to get stuck in.
	import { Transcribe } from '$lib/api.js';
	import Icon from '$lib/Icon.svelte';
	import { toast } from '$lib/toast.svelte.js';

	let { ontext, onhold, disabled = false } = $props();

	let recording = $state(false);
	let working = $state(false);
	let secs = $state(0);
	let supported = $state(true);

	let rec = null;
	let chunks = [];
	let stream = null;
	let tick = null;

	$effect(() => {
		supported =
			typeof navigator !== 'undefined' &&
			!!navigator.mediaDevices?.getUserMedia &&
			typeof MediaRecorder !== 'undefined';
	});

	// Whatever the browser will actually produce. Safari gives mp4/aac and
	// Chrome webm/opus; ffmpeg on the worker normalises both, so the only
	// thing that matters here is picking one it supports.
	function pickType() {
		for (const t of ['audio/webm;codecs=opus', 'audio/webm', 'audio/mp4', 'audio/aac']) {
			if (MediaRecorder.isTypeSupported?.(t)) return t;
		}
		return '';
	}

	async function start() {
		if (recording || working || disabled) return;
		// Before anything else: touching the mic silences whatever is being
		// said. Waiting for permission or the first byte would make barge-in
		// feel late, which is the same as not having it.
		onhold?.();
		try {
			stream = await navigator.mediaDevices.getUserMedia({
				audio: { echoCancellation: true, noiseSuppression: true }
			});
		} catch {
			toast('Microphone permission is off');
			return;
		}
		chunks = [];
		const mimeType = pickType();
		rec = new MediaRecorder(stream, mimeType ? { mimeType } : undefined);
		rec.ondataavailable = (e) => e.data.size && chunks.push(e.data);
		rec.onstop = finish;
		rec.start();
		recording = true;
		secs = 0;
		tick = setInterval(() => {
			secs += 1;
			// A dictated question is not a lecture. Capping it keeps the
			// upload small and the wait short.
			if (secs >= 60) stop();
		}, 1000);
	}

	function stop() {
		if (!recording) return;
		recording = false;
		clearInterval(tick);
		try {
			rec?.stop();
		} catch {
			/* already stopped */
		}
	}

	async function finish() {
		stream?.getTracks().forEach((t) => t.stop());
		stream = null;
		const blob = new Blob(chunks, { type: rec?.mimeType || 'audio/webm' });
		chunks = [];
		// Under about 8KB is a tap, not speech.
		if (blob.size < 8000) return;

		working = true;
		try {
			const b64 = await toBase64(blob);
			const res = await Transcribe.send(blob.type, b64);
			if (res.text?.trim()) ontext?.(res.text.trim());
			else if (res.queued) toast(res.online ? 'Your Mac is busy — try again' : 'Your Mac is asleep');
			else toast('Nothing was heard');
		} catch (e) {
			toast(e.message || 'Could not transcribe');
		} finally {
			working = false;
		}
	}

	const toBase64 = (blob) =>
		new Promise((res, rej) => {
			const r = new FileReader();
			r.onload = () => res(String(r.result).split(',')[1]);
			r.onerror = rej;
			r.readAsDataURL(blob);
		});
</script>

{#if supported}
	<button
		class="mic"
		class:rec={recording}
		type="button"
		aria-label={recording ? 'Release to send' : 'Hold to speak'}
		disabled={disabled || working}
		onpointerdown={start}
		onpointerup={stop}
		onpointerleave={stop}
		onpointercancel={stop}
		oncontextmenu={(e) => e.preventDefault()}
	>
		{#if working}
			<span class="spinner"></span>
		{:else if recording}
			<span class="rectime">{secs}s</span>
		{:else}
			<Icon name="mic" size={19} />
		{/if}
	</button>
{/if}
