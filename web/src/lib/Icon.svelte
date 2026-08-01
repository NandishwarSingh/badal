<script module>
	// One place for every glyph in the app.
	//
	// These were text characters -- ▤ ✉ ◇ ☑ ◔ ↻ -- which is convenient and
	// wrong. A character renders in whatever the platform has: different
	// weights and baselines on iOS, Android and macOS, some of them emoji-fied
	// into full colour, and none of them sitting on the same optical grid as
	// the rest of the interface. Paths render identically everywhere and
	// inherit stroke width from the design system like every other line.
	//
	// 24×24, stroked, no fills, so they scale and take currentColor.
	export const PATHS = {
		note: 'M6 3h9l4 4v14H6z M15 3v4h4 M9.5 12h6 M9.5 16h4',
		mail: 'M3.5 6.5h17v11h-17z M3.5 7l8.5 6 8.5-6',
		event: 'M4 6h16v14H4z M4 10h16 M8 3.5v4 M16 3.5v4',
		task: 'M4.5 5.5h15v13h-15z M8.5 12l2.5 2.5 4.5-5',
		clock: 'M12 4a8 8 0 1 0 0 16 8 8 0 0 0 0-16z M12 7.5V12l3 1.8',
		bell: 'M18 8.5a6 6 0 1 0-12 0c0 5-2 6.5-2 6.5h16s-2-1.5-2-6.5 M10.5 19a2 2 0 0 0 3 0',
		repeat: 'M4 11a7 7 0 0 1 12-4.5L19 9 M19 4.5V9h-4.5 M20 13a7 7 0 0 1-12 4.5L5 15 M5 19.5V15h4.5',
		alert: 'M12 8v5 M12 16.5v.5 M12 3.5 2.5 20h19z',
		review: 'M3.5 13.5h5l1.5 2.5h4l1.5-2.5h5 M5.5 5h13l2 8.5v5h-17v-5z',
		digest: 'M12 7.5a4.5 4.5 0 1 0 0 9 4.5 4.5 0 0 0 0-9z M12 2.5v2 M12 19.5v2 M2.5 12h2 M19.5 12h2 M5.2 5.2l1.4 1.4 M17.4 17.4l1.4 1.4 M18.8 5.2l-1.4 1.4 M6.6 17.4l-1.4 1.4',
		gear: 'M12 9a3 3 0 1 0 0 6 3 3 0 0 0 0-6z M19.4 15a1.6 1.6 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.6 1.6 0 0 0-2.7 1.1v.3a2 2 0 1 1-4 0v-.2a1.6 1.6 0 0 0-2.8-1.1l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1A1.6 1.6 0 0 0 3.5 15a2 2 0 1 1 0-4h.2a1.6 1.6 0 0 0 1.1-2.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1A1.6 1.6 0 0 0 10.5 4.5a2 2 0 1 1 4 0v.2a1.6 1.6 0 0 0 2.7 1.1l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.6 1.6 0 0 0 1.1 2.7h.3a2 2 0 1 1 0 4h-.2z',
		chevron: 'M9.5 5.5 16 12l-6.5 6.5',
		close: 'M6.5 6.5l11 11 M17.5 6.5l-11 11',
		search: 'M11 4a7 7 0 1 0 0 14 7 7 0 0 0 0-14z M20 20l-4.2-4.2',
		pin: 'M12 21s6.5-6.1 6.5-10.5a6.5 6.5 0 1 0-13 0C5.5 14.9 12 21 12 21z M12 8.2a2.3 2.3 0 1 0 0 4.6 2.3 2.3 0 0 0 0-4.6z',
		check: 'M4.5 12.5l5 5 10-11',
		mute: 'M11 5.5 6.8 9H4v6h2.8L11 18.5z M16 9.5l4 5 M20 9.5l-4 5',
		mic: 'M12 3.5a2.6 2.6 0 0 0-2.6 2.6v6a2.6 2.6 0 0 0 5.2 0v-6A2.6 2.6 0 0 0 12 3.5z M6 11a6 6 0 0 0 12 0 M12 17v3.5'
	};
</script>

<script>
	let { name, size = 16, stroke = 1.9, label = '' } = $props();
</script>

<svg
	class="ic"
	viewBox="0 0 24 24"
	width={size}
	height={size}
	fill="none"
	stroke="currentColor"
	stroke-width={stroke}
	stroke-linecap="round"
	stroke-linejoin="round"
	role={label ? 'img' : 'presentation'}
	aria-label={label || undefined}
	aria-hidden={label ? undefined : 'true'}
>
	<path d={PATHS[name] ?? PATHS.note} />
</svg>
