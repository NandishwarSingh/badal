// Horizontal swipe to move between the four tabs.
//
// The content follows the finger, then either springs back or hands off to a
// direction-aware page transition. Two rules keep it from fighting the page:
//   - Do not claim the gesture until horizontal travel clearly beats vertical,
//     or every attempt to scroll turns into a tab change.
//   - Commit on velocity as well as distance, so a flick works without
//     dragging all the way across the screen.

const CLAIM = 10; // px before the gesture is ours
const THRESHOLD = 0.28; // fraction of width for a slow drag
const FLICK = 0.4; // px/ms that commits regardless of distance
const RESIST = 0.28; // rubber-banding at the ends of the tab list

/**
 * @param {HTMLElement} node
 * @param {{onNext?:()=>void, onPrev?:()=>void, hasNext?:boolean, hasPrev?:boolean}} opts
 */
export function swipeNav(node, opts = {}) {
	let startX = 0,
		startY = 0,
		startT = 0,
		dx = 0;
	let active = false,
		decided = false,
		claimed = false;

	const width = () => window.innerWidth || 390;

	function paint(x, animate) {
		node.style.transition = animate ? 'transform .34s cubic-bezier(.22,.61,.36,1)' : 'none';
		node.style.transform = x ? `translate3d(${x}px,0,0)` : '';
	}

	function down(e) {
		// Never hijack a drag that starts inside something scrollable
		// horizontally, or inside a text field.
		const t = e.target;
		if (t.closest('input, textarea, pre, [data-noswipe]')) return;
		const p = e.touches ? e.touches[0] : e;
		startX = p.clientX;
		startY = p.clientY;
		startT = performance.now();
		dx = 0;
		active = true;
		decided = false;
		claimed = false;
	}

	function move(e) {
		if (!active) return;
		const p = e.touches ? e.touches[0] : e;
		const ax = p.clientX - startX;
		const ay = p.clientY - startY;

		if (!decided) {
			if (Math.abs(ax) < CLAIM && Math.abs(ay) < CLAIM) return;
			decided = true;
			// Vertical intent wins; hand the gesture back to the scroller.
			claimed = Math.abs(ax) > Math.abs(ay) * 1.25;
			if (!claimed) {
				active = false;
				return;
			}
		}
		if (!claimed) return;
		if (e.cancelable) e.preventDefault();

		// Rubber-band when there is no tab that way.
		const wall = (ax < 0 && !opts.hasNext) || (ax > 0 && !opts.hasPrev);
		dx = wall ? ax * RESIST : ax;
		paint(dx, false);
	}

	function up() {
		if (!active) return;
		active = false;
		if (!claimed) return;

		const v = Math.abs(dx) / Math.max(1, performance.now() - startT);
		const go = Math.abs(dx) > width() * THRESHOLD || (v > FLICK && Math.abs(dx) > 40);
		const next = dx < 0;

		if (go && ((next && opts.hasNext) || (!next && opts.hasPrev))) {
			// Let the view transition own the rest of the motion; clearing the
			// inline transform first stops the two fighting each other.
			paint(0, false);
			(next ? opts.onNext : opts.onPrev)?.();
			return;
		}
		paint(0, true);
	}

	node.addEventListener('touchstart', down, { passive: true });
	node.addEventListener('touchmove', move, { passive: false });
	node.addEventListener('touchend', up, { passive: true });
	node.addEventListener('touchcancel', up, { passive: true });

	return {
		update(next) {
			opts = next || {};
		},
		destroy() {
			node.removeEventListener('touchstart', down);
			node.removeEventListener('touchmove', move);
			node.removeEventListener('touchend', up);
			node.removeEventListener('touchcancel', up);
		}
	};
}
