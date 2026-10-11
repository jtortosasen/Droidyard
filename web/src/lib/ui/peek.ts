// The sessions overlay's model (m2f D43, D44): when the undocked sessions column shows as
// an overlay, from geometry alone. Where the pointer is arrives with each coalesced
// pointermove (`pointer`), never as enter and leave events: an overlay that appears under
// a still pointer gets no `pointerenter` (D43). Time is an injected scheduler, so tests
// run on fake time. It knows nothing of `Layout`: the shell feeds it `setDocked` and
// `setWidth` and draws `view`.
import { edgeZone, FEINT_PX } from './edge';

/** The pointer rests this long on the toggle before the overlay opens (D43). */
export const INTENT_MS = 150;
/** The pointer is outside this long before the overlay hides (D43). */
export const GRACE_MS = 300;

export interface Scheduler {
	setTimeout(fn: () => void, ms: number): unknown;
	clearTimeout(handle: unknown): void;
}

const realTime: Scheduler = {
	setTimeout: (fn, ms) => globalThis.setTimeout(fn, ms),
	clearTimeout: (h) => globalThis.clearTimeout(h as ReturnType<typeof setTimeout>)
};

export interface PointerSample {
	/** clientX. */
	x: number;
	/** clientY lies in the band; false while a modal dialog is open (D44). */
	inBand: boolean;
	/** The pointer is over the sessions toggle's rect. */
	onToggle: boolean;
	buttons: number;
	pointerType: string;
}

export type PeekState = 'hidden' | 'feint' | 'shown';

export interface PeekView {
	state: PeekState;
	/** px the overlay's edge is out in the feint. */
	offset: number;
	/** 0..1: the toggle's `--attract`. */
	attract: number;
}

export class PeekModel {
	#sched: Scheduler;
	#state: PeekState = 'hidden';
	#offset = 0;
	#attract = 0;
	#width = 0;
	#docked = false;
	#reduced = false;
	#focusInside = false;
	#armed = true;
	// The last sample's geometry. `#wasOnToggle` decides "arriving at the toggle from off it".
	#inside = false;
	#wasOnToggle = false;
	// Set on undocking; only a pointer off the toggle clears it (D43).
	#latch = false;
	#hover: unknown = null;
	#grace: unknown = null;
	#view: PeekView | null = null;
	#listeners = new Set<() => void>();

	constructor(scheduler: Scheduler = realTime) {
		this.#sched = scheduler;
	}

	get view(): PeekView {
		this.#view ??= { state: this.#state, offset: this.#offset, attract: this.#attract };
		return this.#view;
	}

	subscribe(listener: () => void): () => void {
		this.#listeners.add(listener);
		return () => this.#listeners.delete(listener);
	}

	setWidth(px: number): void {
		this.#width = px;
	}

	setReducedMotion(b: boolean): void {
		this.#reduced = b;
	}

	setFocusInside(b: boolean): void {
		this.#focusInside = b;
		if (this.#docked || this.#state !== 'shown') return;
		if (b) this.#clearGrace();
		else if (!this.#inside) this.#startGrace();
	}

	setDocked(b: boolean): void {
		if (b === this.#docked) return;
		this.#docked = b;
		this.#clearHover();
		if (b) {
			this.#hide();
			return;
		}
		// D43/D44: undocking never reopens the overlay or draws a feint under a still
		// pointer. The edge is disarmed, and the latch holds the hover delay until the
		// pointer has been off the toggle; what was recorded while docked is forgotten.
		this.#armed = false;
		this.#latch = true;
		this.#wasOnToggle = false;
	}

	pointer(p: PointerSample): void {
		// D43: only a mouse drives the overlay and the edge.
		if (p.pointerType !== 'mouse') return;
		if (this.#docked) {
			// D43: recording continues while docked; only the decisions are suppressed.
			this.#wasOnToggle = p.onToggle;
			if (p.x > FEINT_PX) this.#armed = true;
			return;
		}
		this.#inside = p.inBand && p.x < this.#width;
		if (p.x > FEINT_PX) this.#armed = true;
		if (!p.onToggle) this.#latch = false;
		const arrived = p.onToggle && !this.#wasOnToggle;
		this.#wasOnToggle = p.onToggle;

		if (this.#state === 'shown') {
			// D44: once shown, D43's hide rules apply, not the distance.
			if (this.#inside || this.#focusInside) this.#clearGrace();
			else this.#startGrace();
			return;
		}

		// D43: a held button never starts the delay nor keeps one running.
		if (!p.onToggle || p.buttons !== 0) this.#clearHover();
		else if (arrived && !this.#latch) this.#startHover();

		const e = edgeZone({
			d: p.x,
			inBand: p.inBand,
			buttons: p.buttons,
			docked: false,
			armed: this.#armed,
			reducedMotion: this.#reduced
		});
		if (e.zone === 'pop') {
			this.#show();
			return;
		}
		this.#set(e.zone === 'feint' ? 'feint' : 'hidden', e.offset, e.intensity);
	}

	/** The pointer left the window: the edge ends, and a shown overlay counts the pointer as outside. */
	windowLeave(): void {
		if (this.#docked) return;
		this.#clearHover();
		this.#wasOnToggle = false;
		this.#inside = false;
		if (this.#state === 'shown') {
			if (!this.#focusInside) this.#startGrace();
		} else this.#set('hidden', 0, 0);
	}

	escape(): void {
		if (this.#docked || this.#state === 'hidden') return;
		this.#hide();
	}

	pointerDownOutside(): void {
		if (this.#docked || this.#state !== 'shown') return;
		this.#hide();
	}

	dispose(): void {
		this.#clearHover();
		this.#clearGrace();
		this.#listeners.clear();
	}

	#show(): void {
		this.#clearHover();
		this.#clearGrace();
		this.#set('shown', 0, 0);
	}

	// D44: after any hide the edge is disarmed until the pointer has been at d > FEINT_PX.
	#hide(): void {
		this.#clearHover();
		this.#clearGrace();
		this.#armed = false;
		this.#focusInside = false;
		this.#set('hidden', 0, 0);
	}

	#startHover(): void {
		this.#clearHover();
		this.#hover = this.#sched.setTimeout(() => {
			this.#hover = null;
			this.#show();
		}, INTENT_MS);
	}

	#startGrace(): void {
		if (this.#grace !== null) return;
		this.#grace = this.#sched.setTimeout(() => {
			this.#grace = null;
			this.#hide();
		}, GRACE_MS);
	}

	#clearHover(): void {
		if (this.#hover !== null) this.#sched.clearTimeout(this.#hover);
		this.#hover = null;
	}

	#clearGrace(): void {
		if (this.#grace !== null) this.#sched.clearTimeout(this.#grace);
		this.#grace = null;
	}

	#set(state: PeekState, offset: number, attract: number): void {
		if (state === this.#state && offset === this.#offset && attract === this.#attract) return;
		this.#state = state;
		this.#offset = offset;
		this.#attract = attract;
		this.#view = null;
		for (const l of [...this.#listeners]) l();
	}
}
