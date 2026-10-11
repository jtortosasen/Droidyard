import { describe, expect, it } from 'vitest';
import { GRACE_MS, INTENT_MS, PeekModel, type PointerSample, type Scheduler } from './peek';

/** Fake time: timers fire only as `advance` moves the clock. */
class Clock implements Scheduler {
	now = 0;
	#next = 1;
	#timers = new Map<number, { at: number; fn: () => void }>();
	setTimeout(fn: () => void, ms: number): unknown {
		const id = this.#next++;
		this.#timers.set(id, { at: this.now + ms, fn });
		return id;
	}
	clearTimeout(h: unknown): void {
		this.#timers.delete(h as number);
	}
	get pending(): number {
		return this.#timers.size;
	}
	advance(ms: number): void {
		const end = this.now + ms;
		for (;;) {
			let due: [number, { at: number; fn: () => void }] | null = null;
			for (const t of this.#timers) if (t[1].at <= end && (!due || t[1].at < due[1].at)) due = t;
			if (!due) break;
			this.#timers.delete(due[0]);
			this.now = due[1].at;
			due[1].fn();
		}
		this.now = end;
	}
}

const TOGGLE_X = 20;
const OFF = 600;

function rig() {
	const clock = new Clock();
	const m = new PeekModel(clock);
	m.setWidth(300);
	const move = (x: number, over: Partial<PointerSample> = {}) =>
		m.pointer({ x, inBand: true, onToggle: false, buttons: 0, pointerType: 'mouse', ...over });
	const onToggle = (over: Partial<PointerSample> = {}) => move(TOGGLE_X, { onToggle: true, ...over });
	return { clock, m, move, onToggle, state: () => m.view.state };
}

describe('PeekModel: opening', () => {
	it('opens after the pointer rests INTENT_MS on the toggle (D43)', () => {
		const { clock, onToggle, state } = rig();
		onToggle();
		clock.advance(INTENT_MS - 1);
		expect(state()).not.toBe('shown');
		clock.advance(1);
		expect(state()).toBe('shown');
	});

	it('never opens when the pointer leaves the toggle before the delay (D43)', () => {
		const { clock, move, onToggle, state } = rig();
		onToggle();
		clock.advance(100);
		move(OFF);
		clock.advance(1000);
		expect(state()).toBe('hidden');
	});

	it('never opens with a button held, however long it rests (D43)', () => {
		const { clock, onToggle, state } = rig();
		onToggle({ buttons: 1 });
		clock.advance(1000);
		expect(state()).not.toBe('shown');
	});

	it('is driven by a mouse only: touch and pen change nothing (D43)', () => {
		const { clock, m, onToggle, move, state } = rig();
		onToggle({ pointerType: 'touch' });
		move(2, { pointerType: 'pen' });
		clock.advance(1000);
		expect(state()).toBe('hidden');
		expect(m.view).toEqual({ state: 'hidden', offset: 0, attract: 0 });
		expect(clock.pending).toBe(0);
	});

	it('pops at x = 2 and stays shown with no further input (D43: inside is geometry, not enter/leave)', () => {
		const { clock, move, state } = rig();
		move(2);
		expect(state()).toBe('shown');
		clock.advance(1000);
		expect(state()).toBe('shown');
	});
});

describe('PeekModel: hiding', () => {
	it('hides GRACE_MS after the pointer is outside (D43)', () => {
		const { clock, move, state } = rig();
		move(2);
		move(OFF);
		clock.advance(GRACE_MS - 1);
		expect(state()).toBe('shown');
		clock.advance(1);
		expect(state()).toBe('hidden');
	});

	it('stays when the pointer returns inside within the grace (D43)', () => {
		const { clock, move, state } = rig();
		move(2);
		move(OFF);
		clock.advance(200);
		move(100);
		clock.advance(1000);
		expect(state()).toBe('shown');
	});

	it('stays while focus is inside, until Escape (D43: the project-root input keeps its text)', () => {
		const { clock, m, move, state } = rig();
		move(2);
		m.setFocusInside(true);
		move(OFF);
		clock.advance(1000);
		expect(state()).toBe('shown');
		m.escape();
		expect(state()).toBe('hidden');
	});

	it('hides when focus leaves while the pointer is outside (D43)', () => {
		const { clock, m, move, state } = rig();
		move(2);
		m.setFocusInside(true);
		move(OFF);
		m.setFocusInside(false);
		clock.advance(GRACE_MS);
		expect(state()).toBe('hidden');
	});

	it('hides on Escape and on a pointerdown outside (D43)', () => {
		const a = rig();
		a.move(2);
		a.m.escape();
		expect(a.state()).toBe('hidden');
		const b = rig();
		b.move(2);
		b.m.pointerDownOutside();
		expect(b.state()).toBe('hidden');
	});

	it('counts the pointer leaving the window as outside when shown (D44)', () => {
		const { clock, m, move, state } = rig();
		move(2);
		m.windowLeave();
		clock.advance(GRACE_MS - 1);
		expect(state()).toBe('shown');
		clock.advance(1);
		expect(state()).toBe('hidden');
	});

	it('disarms the edge after a hide until the pointer has been past the feint zone (D44)', () => {
		const { m, move, state } = rig();
		move(2);
		m.escape();
		move(3);
		expect(state()).toBe('hidden');
		move(30);
		move(2);
		expect(state()).toBe('shown');
	});
});

describe('PeekModel: the feint', () => {
	it('retracts when the pointer moves out of the feint zone (D44)', () => {
		const { m, move } = rig();
		move(14);
		expect(m.view.state).toBe('feint');
		expect(m.view.offset).toBeCloseTo(3.5, 10);
		move(40);
		expect(m.view.state).toBe('hidden');
		expect(m.view.offset).toBe(0);
	});

	it('ends with the pointer leaving the window (D44)', () => {
		const { m, move } = rig();
		move(14);
		m.windowLeave();
		expect(m.view).toEqual({ state: 'hidden', offset: 0, attract: 0 });
	});

	it('carries the attract intensity before the feint (D44)', () => {
		const { m, move } = rig();
		move(60);
		expect(m.view).toEqual({ state: 'hidden', offset: 0, attract: 0.5 });
	});

	it('draws no feint with reduced motion (D44)', () => {
		const { m, move } = rig();
		m.setReducedMotion(true);
		move(14);
		expect(m.view).toEqual({ state: 'hidden', offset: 0, attract: 1 });
	});
});

describe('PeekModel: docked', () => {
	it('hides when docked and ignores every input while docked (D43)', () => {
		const { clock, m, move, onToggle, state } = rig();
		move(2);
		m.setDocked(true);
		expect(state()).toBe('hidden');
		move(2);
		onToggle();
		m.escape();
		m.windowLeave();
		clock.advance(1000);
		expect(m.view).toEqual({ state: 'hidden', offset: 0, attract: 0 });
		expect(clock.pending).toBe(0);
	});

	it('does not reopen or feint under a still pointer when undocked (D43, D44: the undock disarm)', () => {
		const { clock, m, move, onToggle, state } = rig();
		onToggle();
		m.setDocked(true);
		move(OFF); // docked, the pointer moves away: armed again
		onToggle(); // and back to the toggle, where the click that undocks happens
		m.setDocked(false);
		clock.advance(1000);
		expect(state()).toBe('hidden');
		// The pointer moves a little at the toggle (x = 20): disarmed, so no feint either.
		onToggle();
		expect(m.view.state).toBe('hidden');
		clock.advance(1000);
		expect(state()).toBe('hidden');
	});

	it('holds the hover delay after undocking until the pointer has been off the toggle (D43: the latch)', () => {
		const { clock, m, move, onToggle, state } = rig();
		m.setDocked(true);
		onToggle();
		m.setDocked(false);
		move(21, { onToggle: true });
		clock.advance(1000);
		expect(state()).toBe('hidden');
		move(OFF);
		onToggle();
		clock.advance(INTENT_MS);
		expect(state()).toBe('shown');
	});
});

describe('PeekModel: lifetime', () => {
	it('dispose clears a pending timer: no callback after it (D43)', () => {
		const { clock, m, onToggle, state } = rig();
		onToggle();
		expect(clock.pending).toBe(1);
		m.dispose();
		expect(clock.pending).toBe(0);
		clock.advance(1000);
		expect(state()).not.toBe('shown');
	});

	it('notifies subscribers only when the view changes', () => {
		const { m, move } = rig();
		let n = 0;
		const off = m.subscribe(() => n++);
		move(OFF);
		move(OFF);
		expect(n).toBe(0);
		move(2);
		expect(n).toBe(1);
		off();
		m.escape();
		expect(n).toBe(1);
	});
});
