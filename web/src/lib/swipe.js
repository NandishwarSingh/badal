// Swipe actions for list rows.
//
// Follows the finger 1:1 while dragging, then springs. Two rules make it feel
// native rather than janky:
//   - Do not claim the gesture until it is clearly horizontal, or vertical
//     scrolling starts fighting the swipe.
//   - Commit on velocity as well as distance, so a fast flick works without
//     dragging the row all the way across.

const THRESHOLD = 0.32; // fraction of width to commit on a slow drag
const FLICK = 0.45; // px/ms that commits regardless of distance
const CLAIM = 9; // px of horizontal travel before we take the gesture

/**
 * @param {HTMLElement} node
 * @param {{onLeft?:()=>void, onRight?:()=>void, disabled?:boolean}} opts
 */
export function swipe(node, opts = {}) {
	let startX = 0,
		startY = 0,
		startT = 0;
	let dx = 0,
		claimed = false,
		decided = false,
		active = false;
	const width = () => node.offsetWidth || 320;

	function setX(x, animate) {
		node.style.transition = animate
			? 'transform .42s cubic-bezier(.34,1.56,.64,1), opacity .3s ease'
			: 'none';
		node.style.transform = x ? `translate3d(${x}px,0,0)` : '';
		node.style.opacity = x ? String(Math.max(0.45, 1 - Math.abs(x) / (width() * 1.7))) : '';
		// Tint the row toward the action being revealed.
		const p = Math.min(1, Math.abs(x) / (width() * THRESHOLD));
		node.style.setProperty('--swipe-progress', String(p));
		node.style.setProperty('--swipe-dir', x < 0 ? '-1' : '1');
	}

	function down(e) {
		if (opts.disabled) return;
		const t = e.touches ? e.touches[0] : e;
		startX = t.clientX;
		startY = t.clientY;
		startT = performance.now();
		dx = 0;
		claimed = false;
		decided = false;
		active = true;
		node.style.transition = 'none';
	}

	function move(e) {
		if (!active) return;
		const t = e.touches ? e.touches[0] : e;
		const ax = t.clientX - startX;
		const ay = t.clientY - startY;

		if (!decided) {
			if (Math.abs(ax) < CLAIM && Math.abs(ay) < CLAIM) return;
			// Vertical intent wins -- let the page scroll.
			decided = true;
			claimed = Math.abs(ax) > Math.abs(ay);
			if (!claimed) {
				active = false;
				setX(0, true);
				return;
			}
		}
		if (!claimed) return;
		if (e.cancelable) e.preventDefault();

		// Resist pulling toward an action that is not wired up.
		const wanted = ax < 0 ? opts.onLeft : opts.onRight;
		dx = wanted ? ax : ax * 0.22;
		setX(dx, false);
	}

	function up() {
		if (!active) return;
		active = false;
		if (!claimed) return;
		const v = Math.abs(dx) / Math.max(1, performance.now() - startT);
		const far = Math.abs(dx) > width() * THRESHOLD;
		const flick = v > FLICK && Math.abs(dx) > 34;
		const fire = dx < 0 ? opts.onLeft : opts.onRight;

		if ((far || flick) && fire) {
			// Fling it off, then hand over to the caller.
			setX(dx < 0 ? -width() : width(), true);
			node.style.opacity = '0';
			setTimeout(fire, 190);
			return;
		}
		setX(0, true);
	}

	node.addEventListener('touchstart', down, { passive: true });
	node.addEventListener('touchmove', move, { passive: false });
	node.addEventListener('touchend', up, { passive: true });
	node.addEventListener('touchcancel', up, { passive: true });
	node.addEventListener('pointerdown', down);
	node.addEventListener('pointermove', move);
	node.addEventListener('pointerup', up);
	node.addEventListener('pointercancel', up);

	return {
		update(next) {
			opts = next || {};
		},
		destroy() {
			node.removeEventListener('touchstart', down);
			node.removeEventListener('touchmove', move);
			node.removeEventListener('touchend', up);
			node.removeEventListener('touchcancel', up);
			node.removeEventListener('pointerdown', down);
			node.removeEventListener('pointermove', move);
			node.removeEventListener('pointerup', up);
			node.removeEventListener('pointercancel', up);
		}
	};
}
