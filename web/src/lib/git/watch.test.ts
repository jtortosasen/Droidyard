import { describe, expect, it } from 'vitest';
import { ConnectionError, type BusEvent, type SubscribeHandlers, type Subscription } from '../api/client';
import { FakeStream, capError, settle } from '../build/testing';
import { GitWatch, RESTART_MS, type GitClient, type GitTimers } from './watch';

class FakeTimers implements GitTimers {
	#next = 1;
	pending = new Map<number, { at: number; fn: () => void }>();
	now = 0;
	setTimeout(fn: () => void, ms: number): unknown {
		const id = this.#next++;
		this.pending.set(id, { at: this.now + ms, fn });
		return id;
	}
	clearTimeout(h: unknown): void {
		this.pending.delete(h as number);
	}
	advance(ms: number): void {
		this.now += ms;
		for (const [id, t] of [...this.pending]) {
			if (t.at <= this.now) {
				this.pending.delete(id);
				t.fn();
			}
		}
	}
}

const status = (paths: string[], oid = 'c1') => ({
	head: { oid, branch: 'main' },
	entries: paths.map((path) => ({ path, mark: 'modified' as const })),
	truncated: false,
});

function setup() {
	const stream = new FakeStream('g');
	const timers = new FakeTimers();
	const listeners = new Set<() => void>();
	const ws = {
		id: 'w1' as string | null,
		root: '/proj',
		subscribe: (l: () => void) => {
			listeners.add(l);
			return () => listeners.delete(l);
		},
	};
	const watch = new GitWatch(stream as unknown as GitClient, ws, timers);
	const answer = async (i: number, paths: string[] = [], oid = 'c1') => {
		stream.answer(i, { subscriptionId: stream.ids[i], base: 'HEAD', ...status(paths, oid) });
		await settle();
	};
	const emit = (i: number, type: string, payload: unknown) =>
		stream.emit(i, { topic: 'git.changes', type, payload } as Partial<BusEvent> & { topic: string; type: string; payload: unknown });
	return { stream, timers, watch, answer, emit };
}

describe('GitWatch', () => {
	// Decision (ADR-0066 §1): the answer is the baseline and every status event is the WHOLE status again, so each replaces the last.
	it('takes the answer as the status and replaces it with each status event', async () => {
		const t = setup();
		expect(t.watch.view.state).toBe('starting');
		await t.answer(0, ['a.kt', 'b.kt']);
		expect(t.watch.view).toMatchObject({ state: 'watching', head: { oid: 'c1', branch: 'main' }, truncated: false });
		expect(t.watch.view.entries.map((e) => e.path)).toEqual(['a.kt', 'b.kt']);
		t.emit(0, 'status', status(['b.kt', 'c.kt'], 'c2'));
		expect(t.watch.view.entries.map((e) => e.path)).toEqual(['b.kt', 'c.kt']);
		expect(t.watch.view.head?.oid).toBe('c2');
		t.emit(0, 'status', { ...status([]), truncated: true });
		expect(t.watch.view.entries).toEqual([]);
		expect(t.watch.view.truncated).toBe(true);
	});

	// Decision: an event that arrives before the answer is applied after it, in order, not lost.
	it('applies an event that came before the answer after the baseline', async () => {
		const t = setup();
		t.emit(0, 'status', status(['early.kt']));
		await t.answer(0, ['base.kt']);
		expect(t.watch.view.entries.map((e) => e.path)).toEqual(['early.kt']);
	});

	// Decision (plan step 5): after `stopped {failed}` the watch starts again after 2 s, not at once and not before, ending the old subscription first.
	it('starts again 2 s after stopped failed', async () => {
		const t = setup();
		await t.answer(0, ['a.kt']);
		t.emit(0, 'stopped', { reason: 'failed' });
		expect(t.watch.view.state).toBe('waiting');
		expect(t.watch.view.entries.map((e) => e.path)).toEqual(['a.kt']); // what was shown stays
		expect(t.stream.unsubscribed).toEqual(['g1']);
		t.timers.advance(RESTART_MS - 1);
		expect(t.stream.handlers).toHaveLength(1);
		t.timers.advance(1);
		expect(t.stream.handlers).toHaveLength(2);
		await t.answer(1, ['z.kt']);
		expect(t.watch.view.state).toBe('watching');
		expect(t.watch.view.entries.map((e) => e.path)).toEqual(['z.kt']);
	});

	// Decision: a closed workspace ends the watch for good: no restart.
	it('does not restart after stopped workspaceClosed', async () => {
		const t = setup();
		await t.answer(0);
		t.emit(0, 'stopped', { reason: 'workspaceClosed' });
		t.timers.advance(10 * RESTART_MS);
		expect(t.watch.view.state).toBe('ended');
		expect(t.stream.handlers).toHaveLength(1);
	});

	// Decision (plan step 5): a project outside a repository (or without git) is text with Retry and does NOT poll the daemon.
	it.each(['GIT_NOT_A_REPOSITORY', 'GIT_NOT_FOUND'])('shows %s as text and tries again only on Retry', async (code) => {
		let calls = 0;
		let fail = true;
		const client = {
			call: async () => ({}),
			subscribe: async () => {
				calls++;
				if (fail) throw capError(code, `no ${code}`);
				return { subscriptionId: 's', result: { subscriptionId: 's', base: 'HEAD', ...status(['a.kt']) }, stop() {} } as Subscription & { result: never };
			},
		};
		const timers = new FakeTimers();
		const ws = { id: 'w1' as string | null, root: '/p', subscribe: () => () => {} };
		const watch = new GitWatch(client as unknown as GitClient, ws, timers);
		await settle();
		expect(calls).toBe(1);
		expect(watch.view.state).toBe('error');
		expect(watch.view.problem?.code).toBe(code);
		expect(watch.view.problem?.text).toContain(`no ${code}`);
		expect(watch.view.entries).toEqual([]);
		timers.advance(60_000);
		await settle();
		expect(calls).toBe(1); // no retry loop
		fail = false;
		watch.retry();
		await settle();
		expect(calls).toBe(2);
		expect(watch.view.state).toBe('watching');
	});

	// Decision (D26, as FileWatch): the watch ends while the page is hidden, in the daemon too, and starts again with a new status when shown.
	it('ends while hidden and watches again when shown', async () => {
		const t = setup();
		await t.answer(0, ['a.kt']);
		t.watch.setHidden(true);
		expect(t.stream.unsubscribed).toEqual(['g1']);
		expect(t.watch.view.state).toBe('paused');
		t.emit(0, 'status', status(['ignored.kt'])); // a late event of the ended watch
		expect(t.watch.view.entries.map((e) => e.path)).toEqual(['a.kt']);
		t.watch.setHidden(false);
		expect(t.stream.handlers).toHaveLength(2);
		await t.answer(1, ['b.kt']);
		expect(t.watch.view.entries.map((e) => e.path)).toEqual(['b.kt']);
	});

	// Decision: hiding the page cancels a restart that was waiting, so a hidden page does not call the daemon.
	it('cancels the restart when the page is hidden', async () => {
		const t = setup();
		await t.answer(0);
		t.emit(0, 'stopped', { reason: 'failed' });
		t.watch.setHidden(true);
		t.timers.advance(10 * RESTART_MS);
		expect(t.stream.handlers).toHaveLength(1);
	});

	// Decision: a lost connection waits for the session to be open again and then watches once more.
	it('watches again after a lost connection once the session is open', async () => {
		const t = setup();
		await t.answer(0, ['a.kt']);
		(t.stream.handlers[0] as SubscribeHandlers).onEnd?.(new ConnectionError('lost'));
		expect(t.watch.view.state).toBe('waiting');
		expect(t.stream.handlers).toHaveLength(1);
		t.watch.onConnection('open');
		expect(t.stream.handlers).toHaveLength(2);
		await t.answer(1, ['b.kt']);
		expect(t.watch.view.entries.map((e) => e.path)).toEqual(['b.kt']);
	});

	it('unsubscribes on dispose', async () => {
		const t = setup();
		await t.answer(0);
		t.watch.dispose();
		expect(t.stream.unsubscribed).toEqual(['g1']);
	});
});
