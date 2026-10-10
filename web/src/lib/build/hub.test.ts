import { describe, expect, it } from 'vitest';
import { ConnectionError } from '../api/client';
import type { LifecycleEvent } from './hub';
import { hubStarted, settle } from './testing';

describe('EventsHub', () => {
	// Decision (D12): one subscription for both topics; progress is looked up by operation id.
	it('subscribes once and routes progress by operation id', async () => {
		const { events, hub } = await hubStarted();
		expect(events.handlers).toHaveLength(1);
		events.emit(0, { topic: 'operation.progress', type: 'progress', operation: 'a', payload: { done: 1, total: 2 } });
		expect(hub.progress('a')).toEqual({ done: 1, total: 2 });
		expect(hub.progress('b')).toBeUndefined();
	});

	// Decision (D10): workspace.lifecycle reaches the listeners with the workspace id and the new state.
	it('hands workspace.lifecycle events to the listeners', async () => {
		const { events, hub } = await hubStarted();
		const got: LifecycleEvent[] = [];
		hub.onLifecycle((e) => got.push(e));
		events.emit(0, { topic: 'workspace.lifecycle', type: 'closed', workspace: 'w1', payload: { state: 'closed' } });
		expect(got).toEqual([{ workspace: 'w1', state: 'closed' }]);
	});

	// Decision: a subscription lost with the connection starts again when it is open, and
	// the panels are told to read the daemon again; the first start is not a resync.
	it('starts again after a lost connection and tells the listeners to resync', async () => {
		const { events, hub } = await hubStarted();
		let resyncs = 0;
		hub.onResync(() => resyncs++);
		events.handlers[0]!.onEnd?.(new ConnectionError('lost'));
		hub.onConnection('open');
		events.answer(1, { subscriptionId: 'ev2' });
		await settle();
		expect(events.handlers).toHaveLength(2);
		expect(resyncs).toBe(1);
	});

	it('tells the listeners to resync on events.gap', async () => {
		const { events, hub } = await hubStarted();
		let resyncs = 0;
		hub.onResync(() => resyncs++);
		events.emit(0, { topic: 'events.gap', type: 'gap', payload: {} });
		expect(resyncs).toBe(1);
	});
});
