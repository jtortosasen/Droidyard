import { describe, expect, it } from 'vitest';
import { ConnectionError } from '../api/client';
import { compileQuery } from './query';
import { DEFAULT_QUERY, loadQuery, QueryBar, queryKey } from './bar';
import type { KeyValueStorage } from '../ui/layout';
import { LogcatSession } from './session';
import { FakeClient, FakeFrames, line, settle } from './testing';

async function make(opts: { capacity?: number; hiddenCap?: number; hidden?: boolean } = {}) {
	const client = new FakeClient();
	const frames = new FakeFrames();
	const session = new LogcatSession({ client, frames, ...opts });
	let changes = 0;
	session.onChange(() => changes++);
	session.select('emulator-5554');
	client.answer(0);
	await settle();
	const text = () =>
		session.rows(0, session.count).map((r) => ('message' in r.entry ? r.entry.message : `[${r.entry.marker}]`));
	return {
		client,
		frames,
		session,
		text,
		get changes() {
			return changes;
		},
		feed(n: number, from = 1) {
			client.lines(
				0,
				Array.from({ length: n }, (_, i) => line(from + i, `m${from + i}`)),
			);
		},
	};
}

describe('LogcatSession', () => {
	// Decision (A7): lines are appended once per animation frame: any number of events in
	// a frame is one move into the list and one notification.
	it('moves everything that arrived into the list once per frame', async () => {
		const t = await make();
		const before = t.changes;
		for (let i = 0; i < 40; i++) t.client.lines(0, [line(i + 1, `m${i}`)]);
		expect(t.session.count).toBe(0);
		expect(t.changes).toBe(before);
		t.frames.tick();
		expect(t.session.count).toBe(40);
		expect(t.changes).toBe(before + 1);
		expect(t.text().slice(0, 2)).toEqual(['m0', 'm1']);
	});

	// Decision (A8): the ring stops at its cap and counts what it forgot; the footer shows both.
	it('holds at most its capacity and counts the lines it forgot', async () => {
		const t = await make({ capacity: 5 });
		t.feed(8);
		t.frames.tick();
		expect(t.session.held).toBe(5);
		expect(t.session.count).toBe(5);
		expect(t.session.drops).toEqual({ daemon: 0, queue: 0, ring: 3 });
		expect(t.text()[0]).toBe('m4');
	});

	it('counts the daemon-reported drops apart', async () => {
		const t = await make();
		t.client.lines(0, [line(1, 'a')], 12);
		t.frames.tick();
		expect(t.session.drops.daemon).toBe(12);
		expect(t.text()).toEqual(['[dropped]', 'a']);
	});

	// Decision: a hidden tab queues at most its cap, and what it dropped is a marker in
	// the list and a count in the footer once the tab is back.
	it('marks and counts what a hidden tab dropped', async () => {
		const t = await make({ hidden: true, hiddenCap: 3 });
		t.feed(10);
		t.session.setHidden(false);
		t.frames.tick();
		expect(t.session.drops.queue).toBe(7);
		expect(t.text()).toEqual(['[hidden]', 'm8', 'm9', 'm10']);
	});

	// Decision: the list can be filtered without the stream restarting, and a filter never
	// hides a marker.
	it('filters the held lines and keeps the markers', async () => {
		const t = await make();
		t.client.lines(0, [line(1, 'keep'), line(2, 'other')], 3);
		t.frames.tick();
		t.session.setFilter(compileQuery('message:keep'));
		expect(t.text()).toEqual(['[dropped]', 'keep']);
		t.client.lines(0, [line(3, 'other 2'), line(4, 'keep 2')]);
		t.frames.tick();
		expect(t.text()).toEqual(['[dropped]', 'keep', 'keep 2']);
		t.session.setFilter(compileQuery(''));
		expect(t.session.count).toBe(5);
	});

	// Decision (D41): crash runs are marked as lines arrive, before the view tests them.
	it('marks crash lines at ingest', async () => {
		const t = await make();
		t.client.lines(0, [
			line(1, 'FATAL EXCEPTION: main', { tag: 'AndroidRuntime', priority: 'error' }),
			line(2, 'java.lang.Boom', { tag: 'AndroidRuntime', priority: 'error' }),
			line(3, 'fine', { tag: 'Other' }),
		]);
		t.frames.tick();
		t.session.setFilter(compileQuery('is:crash'));
		expect(t.text()).toEqual(['FATAL EXCEPTION: main', 'java.lang.Boom']);
	});

	// Decision: when the user scrolled away from the bottom the list keeps growing but
	// the pill counts what arrived since; back at the bottom it is zero.
	it('counts the lines that arrived below the view when not following', async () => {
		const t = await make();
		t.feed(10);
		t.frames.tick();
		expect(t.session.newCount).toBe(0);
		t.session.setFollowing(false);
		t.feed(4, 11);
		t.frames.tick();
		expect(t.session.count).toBe(14);
		expect(t.session.newCount).toBe(4);
		t.session.setFollowing(true);
		expect(t.session.newCount).toBe(0);
	});

	// Decision: a new filter starts a new list, so the view goes back to the bottom.
	it('follows again, with no new lines, when the filter changes', async () => {
		const t = await make();
		t.feed(6);
		t.frames.tick();
		t.session.setFollowing(false);
		t.feed(2, 7);
		t.frames.tick();
		expect(t.session.newCount).toBe(2);
		t.session.setFilter(compileQuery('message:m'));
		expect(t.session.following).toBe(true);
		expect(t.session.newCount).toBe(0);
	});

	// Decision: pause freezes what is shown; the stream and the ring go on, and resuming
	// shows everything and follows again.
	it('freezes the list while paused and shows the rest on resume', async () => {
		const t = await make();
		t.feed(5);
		t.frames.tick();
		t.session.setPaused(true);
		t.feed(3, 6);
		t.frames.tick();
		expect(t.session.paused).toBe(true);
		expect(t.session.count).toBe(5);
		expect(t.session.newCount).toBe(3);
		expect(t.session.held).toBe(8);
		t.session.setPaused(false);
		expect(t.session.count).toBe(8);
		expect(t.session.newCount).toBe(0);
		expect(t.session.following).toBe(true);
	});

	// Decision: clear forgets the held lines, the queue and the counts, and the stream
	// goes on: lines queued before the clear do not reappear.
	it('clears the list, the queue and the counts, and keeps streaming', async () => {
		const t = await make({ capacity: 3 });
		t.feed(5);
		t.frames.tick();
		expect(t.session.drops.ring).toBe(2);
		t.feed(2, 6); // queued, not yet in the list
		t.session.clear();
		t.frames.tick();
		expect(t.session.count).toBe(0);
		expect(t.session.held).toBe(0);
		expect(t.session.drops).toEqual({ daemon: 0, queue: 0, ring: 0 });
		t.feed(1, 9);
		t.frames.tick();
		expect(t.text()).toEqual(['m9']);
	});

	// Decision: when the ring forgets rows the user has scrolled away from, the list
	// reports how many rows left the front so the scroll position can follow the content.
	it('reports how many rows left the front of the list', async () => {
		const t = await make({ capacity: 5 });
		t.feed(5);
		t.frames.tick();
		expect(t.session.takeShift()).toBe(0);
		t.feed(3, 6);
		t.frames.tick();
		expect(t.session.takeShift()).toBe(3);
		expect(t.session.takeShift()).toBe(0);
	});

	// Decision (m2e D40): the stream is always the whole device, filtered by the UI; the
	// session has no application id to pass, and selecting the same device is no restart.
	it('never passes an applicationId and does not restart for the same device', async () => {
		const t = await make();
		t.feed(3);
		t.frames.tick();
		t.session.select('emulator-5554');
		expect(t.client.watches).toHaveLength(1);
		expect(t.client.watches[0]!.params).toEqual({ device: 'emulator-5554' });
		t.session.select('emulator-5556');
		expect(t.client.unsubscribed).toEqual(['sub1']);
		expect(t.client.watches[1]!.params).toEqual({ device: 'emulator-5556' });
		expect(t.session.count).toBe(0);
	});

	it('stops the stream when no device is selected', async () => {
		const t = await make();
		t.session.select('');
		expect(t.client.unsubscribed).toEqual(['sub1']);
		expect(t.session.status.state).toBe('idle');
	});

	// Decision (D4): the session resumes by itself after a reconnect, without clearing
	// the list, and the lines at the boundary do not repeat.
	it('resumes after a reconnect without repeating the boundary', async () => {
		const t = await make();
		t.feed(3);
		t.frames.tick();
		t.client.watches[0]!.handlers.onEnd?.(new ConnectionError('lost'));
		t.session.onConnection('open');
		t.client.answer(1);
		await settle();
		t.client.lines(1, [line(3, 'm3'), line(4, 'm4')]);
		t.frames.tick();
		expect(t.text()).toEqual(['m1', 'm2', 'm3', 'm4']);
		expect(t.client.watches[1]!.params.since).toBe(line(3, '').time);
	});
});

describe('the query bar (m2e D38)', () => {
	const mem = (init: Record<string, string> = {}): KeyValueStorage & { data: Record<string, string> } => ({
		data: init,
		getItem(k) {
			return this.data[k] ?? null;
		},
		setItem(k, v) {
			this.data[k] = v;
		},
	});
	async function withBar(storage: KeyValueStorage | null = mem()) {
		const t = await make();
		const mine = new Set<string>();
		const bar = new QueryBar({ session: t.session, mine, storage, workspaceId: 'w1' });
		const feedLines = (...ls: ReturnType<typeof line>[]) => {
			t.client.lines(0, ls);
			t.frames.tick();
		};
		return { ...t, bar, mine, feedLines };
	}

	// Decision (D38): a query with an error shows the error with its position and the last
	// valid query stays applied (a typo must not silently change what is shown).
	it('keeps the previous filter and its rows while the text has an error', async () => {
		const t = await withBar();
		t.bar.set('message:keep');
		t.feedLines(line(1, 'keep this'), line(2, 'drop this'));
		expect(t.text()).toEqual(['keep this']);
		t.bar.set('message:keep level:loud');
		expect(t.bar.view.error).toMatchObject({ at: expect.any(Number) });
		expect(t.bar.view.error!.message).toContain('level');
		expect(t.text()).toEqual(['keep this']);
		t.feedLines(line(3, 'keep too'), line(4, 'nope'));
		expect(t.text()).toEqual(['keep this', 'keep too']);
		t.bar.set('message:drop');
		expect(t.bar.view.error).toBeNull();
		expect(t.text()).toEqual(['drop this']);
	});

	// Decision (D38): package:mine is the default, and what the bar shows tells when the set is empty.
	it('starts at package:mine and reports that it uses mine', async () => {
		const t = await withBar();
		expect(t.bar.view).toMatchObject({ text: DEFAULT_QUERY, error: null, usesMine: true });
		t.bar.set('tag:x');
		expect(t.bar.view.usesMine).toBe(false);
	});

	// Decision (D39): the set is read live; reapply tests the held lines against the new ids.
	it('shows a held line once its package joins mine', async () => {
		const t = await withBar();
		t.feedLines(line(1, 'a', { package: 'dev.app' }));
		expect(t.text()).toEqual([]);
		t.mine.add('dev.app');
		t.bar.reapply();
		expect(t.text()).toEqual(['a']);
	});

	// Decision (D38): the query is remembered per project under droidyard.ui.logcat.<id>.
	it('restores the remembered query of its own project only', async () => {
		const st = mem({ [queryKey('w1')]: 'tag:abc', [queryKey('w2')]: 'tag:other' });
		const t = await withBar(st);
		expect(t.bar.view.text).toBe('tag:abc');
		t.bar.set('level:error');
		expect(st.data[queryKey('w1')]).toBe('level:error');
		expect(st.data[queryKey('w2')]).toBe('tag:other');
		t.bar.set('level:bogus');
		expect(st.data[queryKey('w1')]).toBe('level:error'); // an invalid text is not remembered
	});

	it('leaves package:mine when the storage throws or is absent', async () => {
		const bad: KeyValueStorage = {
			getItem() {
				throw new Error('blocked');
			},
			setItem() {
				throw new Error('blocked');
			},
		};
		expect(loadQuery(bad, 'w1')).toBe(DEFAULT_QUERY);
		expect(loadQuery(null, 'w1')).toBe(DEFAULT_QUERY);
		const t = await withBar(bad);
		expect(t.bar.view.text).toBe(DEFAULT_QUERY);
		expect(() => t.bar.set('tag:x')).not.toThrow();
	});
});
