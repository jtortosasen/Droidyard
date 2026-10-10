<script lang="ts">
	// A draggable, keyboard-operable bar between two regions (plan m2d D25). It reports
	// the size the region should have; `clamp` keeps it inside the region's bounds, so
	// neither the pointer nor the arrow keys can leave them.
	interface Props {
		/** `x`: a vertical bar that resizes a width; `y`: a horizontal bar that resizes a height. */
		axis: 'x' | 'y';
		/** +1 when dragging towards the end (right, down) grows the region, -1 when it shrinks it. */
		sign: 1 | -1;
		value: number;
		clamp: (n: number) => number;
		onchange: (n: number) => void;
		label: string;
	}
	let { axis, sign, value, clamp, onchange, label }: Props = $props();

	let start: { at: number; size: number } | null = null;

	function down(e: PointerEvent) {
		start = { at: axis === 'x' ? e.clientX : e.clientY, size: value };
		(e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
	}
	function move(e: PointerEvent) {
		if (start === null) return;
		const now = axis === 'x' ? e.clientX : e.clientY;
		onchange(clamp(start.size + sign * (now - start.at)));
	}
	function up() {
		start = null;
	}
	function key(e: KeyboardEvent) {
		const grow = axis === 'x' ? 'ArrowRight' : 'ArrowDown';
		const shrink = axis === 'x' ? 'ArrowLeft' : 'ArrowUp';
		const step = e.shiftKey ? 48 : 12;
		if (e.key === grow) onchange(clamp(value + sign * step));
		else if (e.key === shrink) onchange(clamp(value - sign * step));
		else return;
		e.preventDefault();
	}
</script>

<div
	class="splitter {axis}"
	role="slider"
	tabindex="0"
	aria-label={label}
	aria-orientation={axis === 'x' ? 'vertical' : 'horizontal'}
	aria-valuenow={value}
	aria-valuemin={clamp(0)}
	aria-valuemax={clamp(Number.MAX_SAFE_INTEGER)}
	onpointerdown={down}
	onpointermove={move}
	onpointerup={up}
	onpointercancel={up}
	onkeydown={key}
></div>

<style>
	.splitter {
		flex: none;
		background: transparent;
		touch-action: none;
		position: relative;
	}
	.splitter.x {
		width: var(--splitter);
		cursor: col-resize;
	}
	.splitter.y {
		height: var(--splitter);
		cursor: row-resize;
	}
	/* The hairline sits in the middle of the grab area. */
	.splitter::after {
		content: '';
		position: absolute;
		background: var(--border);
	}
	.x::after {
		inset: 0 auto 0 50%;
		width: 1px;
	}
	.y::after {
		inset: 50% 0 auto 0;
		height: 1px;
	}
	.splitter:hover::after,
	.splitter:focus-visible::after {
		background: var(--accent);
	}
	.splitter:focus-visible {
		outline: none;
	}
</style>
