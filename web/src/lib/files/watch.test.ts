import { describe, expect, it } from 'vitest';
import { ConnectionError, type BusEvent } from '../api/client';
import { FileWatch, CUT, type Baseline, type Change, type WatchClient, type WatchSink, type WatchState } from './watch';
import { FakeStream, settle } from '../build/testing';
import { FakeFrames } from '../logcat/testing';

class Sink implements WatchSink {
	log: string[] = [];
	batches: Change[][] = [];
	baselines: boolean[] = [];
	states: WatchState[] = [];
	reset() {
		this.log.push('reset');
	}
	baseline(_e: Baseline, complete: boolean) {
		this.log.push('baseline');
		this.baselines.push(complete);
	}
	changes(list: Change[]) {
		this.log.push('changes');
		this.batches.push(list);
	}
	cut() {
		this.log.push('cut');
	}
	status(s: WatchState) {
		this.states.push(s);
	}
}

async function setup(opts: { truncated?: boolean } = {}) {
	const stream = new FakeStream('fw');
	const sink = new Sink();
	const frames = new FakeFrames();
	const listeners = new Set<() => void>();
	const ws = {
		id: 'w1' as string | null,
		root: '/proj',
		subscribe: (l: () => void) => {
			listeners.add(l);
			return () => listeners.delete(l);
		},
	};
	const watch = new FileWatch(stream as unknown as WatchClient, ws, sink, frames);
	const answer = async (i: number, truncated = opts.truncated === true) => {
		stream.answer(i, { subscriptionId: stream.ids[i], watched: 0, excluded: [], entries: [], ...(truncated ? { truncated: true } : {}) });
		await settle();
	};
	await answer(0);
	const emit = (i: number, type: string, payload: unknown) =>
		stream.emit(i, { topic: 'file.changes', type, payload } as Partial<BusEvent> & { topic: string; type: string; payload: unknown });
	const changes = (n: number, truncated: boolean) =>
		Array.from({ length: n }, (_, k) => ({ path: `f${k}`, kind: 'created' as const, type: 'file' as const }));
	return { stream, sink, frames, watch, answer, emit, changes };
}

describe('FileWatch', () => {
	// Decision (plan Q3): a truncated event while the baseline was complete means the
	// events no longer add up to the tree: the old subscription ends, then one re-watch.
	it('ends the subscription and watches once more on a truncated event with a complete baseline', async () => {
		const t = await setup();
		t.emit(0, 'changes', { path: '.', changes: t.changes(3, true), truncated: true });
		expect(t.stream.unsubscribed).toEqual(['fw1']);
		expect(t.stream.handlers).toHaveLength(2);
		await t.answer(1);
		expect(t.sink.baselines).toEqual([true, true]);
		expect(t.sink.batches).toEqual([]); // the cut events are not applied
	});

	// Decision: with an incomplete baseline every event says truncated, so the flag is
	// ignored: a small event is applied as it is, with no listing and no re-watch.
	it('applies a small event of an incomplete baseline and neither lists nor watches again', async () => {
		const t = await setup({ truncated: true });
		t.emit(0, 'changes', { path: '.', changes: t.changes(10, true), truncated: true });
		t.frames.tick();
		expect(t.sink.batches.map((b) => b.length)).toEqual([10]);
		expect(t.sink.log).not.toContain('cut');
		expect(t.stream.handlers).toHaveLength(1);
		expect(t.stream.unsubscribed).toEqual([]);
	});

	// Decision: an event of the daemon's cut size means changes were lost: the sink
	// lists again, still without a new watch.
	it('asks the sink to list again on an event of the cut size, without a re-watch', async () => {
		const t = await setup({ truncated: true });
		t.emit(0, 'changes', { path: '.', changes: t.changes(CUT, true), truncated: true });
		t.frames.tick();
		expect(t.sink.log.slice(-2)).toEqual(['changes', 'cut']);
		expect(t.stream.handlers).toHaveLength(1);
	});

	// Decision (D26): events are applied at most once per frame, in order.
	it('hands the events of one frame to the sink together', async () => {
		const t = await setup();
		t.emit(0, 'changes', { path: '.', changes: t.changes(2, false), truncated: false });
		t.emit(0, 'changes', { path: '.', changes: t.changes(1, false), truncated: false });
		expect(t.sink.batches).toEqual([]);
		expect(t.frames.pending).toBe(1);
		t.frames.tick();
		expect(t.sink.batches.map((b) => b.length)).toEqual([3]);
	});

	// Decision (D26, N1): disposing ends the subscription in the daemon.
	it('unsubscribes on dispose and drops what was queued', async () => {
		const t = await setup();
		t.emit(0, 'changes', { path: '.', changes: t.changes(2, false), truncated: false });
		t.watch.dispose();
		expect(t.stream.unsubscribed).toEqual(['fw1']);
		t.frames.tick();
		expect(t.sink.batches).toEqual([]);
	});

	// Decision (D26): the watch ends while the page is hidden and starts again, with a
	// new baseline, when it is shown.
	it('unsubscribes while hidden and watches again when shown', async () => {
		const t = await setup();
		t.watch.setHidden(true);
		expect(t.stream.unsubscribed).toEqual(['fw1']);
		expect(t.stream.handlers).toHaveLength(1);
		t.watch.setHidden(false);
		expect(t.stream.handlers).toHaveLength(2);
		await t.answer(1);
		expect(t.sink.baselines).toHaveLength(2);
	});

	// Decision (plan Q3): a lost connection waits for the session to open, then watches again.
	it('watches again after a lost connection once the session is open', async () => {
		const t = await setup();
		t.stream.handlers[0]!.onEnd?.(new ConnectionError('lost'));
		expect(t.sink.states.at(-1)).toBe('waiting');
		expect(t.stream.handlers).toHaveLength(1);
		t.watch.onConnection('open');
		expect(t.stream.handlers).toHaveLength(2);
	});

	// Decision: a stopped watch starts again, except when its workspace was closed.
	it('watches again on stopped, but not for a closed workspace', async () => {
		const t = await setup();
		t.emit(0, 'stopped', { path: '.', reason: 'unreadable' });
		expect(t.stream.handlers).toHaveLength(2);
		await t.answer(1);
		t.emit(1, 'stopped', { path: '.', reason: 'workspaceClosed' });
		expect(t.stream.handlers).toHaveLength(2);
		expect(t.stream.unsubscribed).toEqual(['fw1', 'fw2']);
	});
});
