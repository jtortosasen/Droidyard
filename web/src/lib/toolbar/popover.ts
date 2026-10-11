// Where a toolbar menu opens: under its trigger, kept inside the window. The toolbar
// clips, the Popover API's top layer does not.
export function placeBelow(anchor: HTMLElement, width: number): { top: number; left: number } {
	const r = anchor.getBoundingClientRect();
	return { top: r.bottom + 4, left: Math.max(8, Math.min(r.left, window.innerWidth - width - 8)) };
}
