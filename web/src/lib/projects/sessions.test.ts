import { describe, expect, it } from 'vitest';
import type { Operation, OperationState } from '../ops/activity';
import { buildBlocks } from '../ops/blocks';
import { FakeStream, settle } from '../build/testing';
import { deviceHolds, duration, heldByText, inUseText, holderOf, keyOf, occupiesText, SessionsModel, YOU, type AgentRow, type SessionsClient, type SessionsSource } from './sessions';

// A source the test drives: the store's operations, newest first, and its listeners.
class Source implements SessionsSource {
	operations: Operation[] = [];
	#ls = new Set<() => void>();
	subscribe(l: () => void) {
		this.#ls.add(l);
		return () => this.#ls.delete(l);
	}
	set(op: Operation) {
		this.operations = [op, ...this.operations.filter((o) => o.id !== op.id)];
		for (const l of [...this.#ls]) l();
	}
}

let n = 0;
function op(over: Partial<Operation> & { state: OperationState }): Operation {
	return {
		id: `op${++n}`,
		capability: 'build.assemble',
		workspace: 'w1',
		client: 'mcp',
		startedAt: '2026-10-10T10:00:00Z',
		...over,
	};
}
const mcp = (id: string, name: string | undefined, workspace?: string) => ({ id, name, workspace });

async function watched() {
	const clock = { t: Date.parse('2026-10-10T10:00:00Z') };
	const src = new Source();
	const stream = new FakeStream('sw');
	const moves: string[] = [];
	const client = {
		call: (n: string, p: unknown) => stream.call(n as 'events.unsubscribe', p as { subscriptionId: string }),
		subscribe: (n: string, p: unknown, h: never) => stream.subscribe(n, p, h),
	} as unknown as SessionsClient;
	let states = 0;
	const model = new SessionsModel(src, () => clock.t, { client, onFollow: (w) => moves.push(w), onState: () => states++ });
	model.start();
	const snapshot = (sessions: unknown[]) => {
		stream.answer(0, { subscriptionId: 'sw1', sessions });
		return settle();
	};
	const ev = (id: string, p: { name?: string; workspace?: string; occupies?: string; devices?: string[]; stale?: boolean; ended?: boolean }) =>
		stream.emit(0, { topic: 'session.state', type: 'state', payload: { id, ended: false, ...p } });
	return { src, model, stream, moves, snapshot, ev, clock, states: () => states };
}
const at = '2026-10-10T10:00:00Z';
const listed = (id: string, name: string | undefined, workspace?: string) => ({ id, ...(name ? { name } : {}), ...(workspace ? { workspace } : {}), connectedAt: at });

describe('SessionsModel: rows are the listed sessions', () => {
	// Decision (ADR-0127 §4, superseding ADR-0048 §3 in part): a row is a session the watch
	// lists, labelled by its name, a repeated name numbered in the order seen. The panel labels
	// among the rows listed now.
	it('shows two listed sessions as two rows, a repeated name numbered in the order seen', async () => {
		const t = await watched();
		await t.snapshot([listed('s1', 'claude'), listed('s2', 'claude'), listed('s3', undefined)]);
		expect(new Map(t.model.view.rows.map((r) => [r.key, r.label]))).toEqual(
			new Map([
				['s1', 'claude'],
				['s2', 'claude 2'],
				['s3', 'agent'],
			]),
		);
	});

	// Decision (ADR-0127 §4): the "You" row goes. A `ui` or `cli` operation makes no row, and a
	// session known only by its operations (not listed) makes none either; the user's write
	// keeps the key YOU for Activity, and a read or watch of the tab has none.
	it('makes no row for the user or for a session the watch does not list', async () => {
		const t = await watched();
		await t.snapshot([]);
		t.src.set(op({ id: 'a', state: 'running', client: 'ui', capability: 'build.assemble' }));
		t.src.set(op({ id: 'b', state: 'running', client: 'cli', capability: 'device.install' }));
		t.src.set(op({ id: 'c', state: 'running', session: mcp('ghost', 'claude-code') }));
		expect(t.model.view.rows).toEqual([]);
		expect(t.model.view.runningAgents).toBe(0);
		expect(keyOf(t.src.operations.find((o) => o.id === 'a')!)).toBe(YOU);
		// The tab's own reads and watches are not activity (m2b D16).
		expect(keyOf(op({ state: 'completed', client: 'ui', capability: 'device.list' }))).toBeNull();
		expect(keyOf(op({ state: 'completed', client: 'ui', capability: 'file.watch' }))).toBeNull();
		// The Sessions tab's own poll is such a read: it is no operation of Activity and no row.
		expect(keyOf(op({ state: 'completed', client: 'ui', capability: 'session.chats' }))).toBeNull();
		const blocks = buildBlocks(
			[op({ id: 'p', state: 'completed', client: 'ui', capability: 'session.chats', session: undefined })],
			{ actor: 'all', agentKey: null },
			{ labels: new Map(), heldBuild: null, ownFailure: null },
		);
		expect(blocks).toEqual([]);
	});

	// Decision (ADR-0083 §1): the watch's workspace is the session's; none is "No project".
	it('places a row under its listed workspace, else under no project', async () => {
		const t = await watched();
		await t.snapshot([listed('s1', 'a', 'wA'), listed('s2', 'b')]);
		expect(Object.fromEntries(t.model.view.rows.map((r) => [r.key, r.workspace]))).toEqual({ s1: 'wA', s2: null });
	});

	// Decision (ADR-0063 §6, kept by ADR-0095 §2): the hook's record is shown in Activity under
	// its session, but it makes a row neither running nor unread.
	it("counts a hook's record for no unread mark", async () => {
		const t = await watched();
		await t.snapshot([listed('s9', 'claude')]);
		t.src.set(op({ id: 'r1', state: 'completed', client: 'cli', capability: 'agent.recordCommand', session: mcp('s9', 'claude') }));
		expect(t.model.view.rows[0]).toMatchObject({ unread: false, running: false, result: null });
	});

	// Decision (D30): running rows first; a row runs while any of its operations is live.
	it('lists running rows first and keeps a row running while any operation is live', async () => {
		const t = await watched();
		await t.snapshot([listed('s1', 'idle-now'), listed('s2', 'busy')]);
		t.src.set(op({ id: 'a1', state: 'running', session: mcp('s1', 'idle-now') }));
		t.src.set(op({ id: 'a1', state: 'completed', session: mcp('s1', 'idle-now') }));
		t.src.set(op({ id: 'b1', state: 'running', session: mcp('s2', 'busy') }));
		t.src.set(op({ id: 'b2', state: 'completed', session: mcp('s2', 'busy') }));
		const rows = t.model.view.rows;
		expect(rows.map((r) => [r.label, r.running])).toEqual([
			['busy', true],
			['idle-now', false],
		]);
		expect(rows[1]!.result).toBe('completed');
	});

	// Decision (D30): unread is a terminal event since the row was last selected; selecting
	// clears it; a terminal event on the selected row is not unread.
	it('marks unread on an unselected row and clears it on select', async () => {
		const t = await watched();
		await t.snapshot([listed('s1', 'a')]);
		t.src.set(op({ id: 'a', state: 'running', session: mcp('s1', 'a') }));
		t.src.set(op({ id: 'a', state: 'failed', session: mcp('s1', 'a') }));
		expect(t.model.view.rows[0]).toMatchObject({ unread: true, result: 'failed' });
		t.model.select('s1');
		expect(t.model.view.rows[0]!.unread).toBe(false);
		t.src.set(op({ id: 'b', state: 'completed', session: mcp('s1', 'a') }));
		expect(t.model.view.rows[0]!.unread).toBe(false);
		t.model.select('other');
		t.src.set(op({ id: 'c', state: 'completed', session: mcp('s1', 'a') }));
		expect(t.model.view.rows[0]!.unread).toBe(true);
	});

	it('counts running sessions', async () => {
		const t = await watched();
		await t.snapshot([listed('s1', 'a'), listed('s2', 'b'), listed('s3', 'c')]);
		t.src.set(op({ id: 'u', state: 'running', client: 'ui' }));
		expect(t.model.view.runningAgents).toBe(0);
		t.src.set(op({ id: 'a', state: 'running', session: mcp('s1', 'a') }));
		t.src.set(op({ id: 'b', state: 'running', session: mcp('s2', 'b') }));
		expect(t.model.view.runningAgents).toBe(2);
	});

	it('lists a running operation with its capability, oldest first', async () => {
		const t = await watched();
		await t.snapshot([listed('s1', 'a')]);
		t.src.set(op({ id: 'a', state: 'running', capability: 'build.assemble', startedAt: '2026-10-10T10:00:00Z', session: mcp('s1', 'a') }));
		t.src.set(op({ id: 'b', state: 'running', capability: 'device.install', startedAt: '2026-10-10T10:00:05Z', session: mcp('s1', 'a') }));
		expect(t.model.view.rows[0]!.operations.map((o) => o.capability)).toEqual(['build.assemble', 'device.install']);
	});

	// Decision (ADR-0127 §4): the chats are read again after a `session.state` event; the model
	// says so through onState, once per event.
	it('tells its owner of each session.state event', async () => {
		const t = await watched();
		await t.snapshot([listed('s1', 'a')]);
		t.ev('s1', { name: 'a', workspace: 'w' });
		t.ev('s1', { name: 'a', workspace: 'w2' });
		expect(t.states()).toBe(2);
	});
});

describe('SessionsModel: two labels (ADR-0127 §4, ADR-0048 §Alternatives)', () => {
	// Decision: the panel numbers among the rows listed now, so a lone `claude-code` reads
	// `claude-code` after two earlier ones ended. A numbering kept over every session ever seen
	// would read `claude-code 3`.
	it('labels the panel row among the rows listed now', async () => {
		const t = await watched();
		await t.snapshot([listed('a', 'claude-code'), listed('b', 'claude-code')]);
		t.ev('a', { ended: true });
		t.ev('b', { ended: true });
		t.ev('c', { name: 'claude-code' });
		expect(t.model.view.rows.map((r) => r.label)).toEqual(['claude-code']);
	});

	// Decision: Activity, the build status and "in use by" read actorLabel, numbered in
	// first-seen order and never renumbered, so an ended session's operations and a live one's
	// are told apart. Reading the panel's label there would give both the name `claude-code`.
	it('keeps an actor label per session ever seen, so two sessions of one name differ', async () => {
		const t = await watched();
		await t.snapshot([listed('a', 'claude-code'), listed('b', 'claude-code')]);
		t.ev('a', { ended: true });
		expect(t.model.view.rows.map((r) => [r.key, r.label])).toEqual([['b', 'claude-code']]);
		expect(t.model.actorLabel('a')).toBe('claude-code');
		expect(t.model.actorLabel('b')).toBe('claude-code 2');
		expect(t.model.actorLabel('unknown')).toBe('agent');
		// A third session never renumbers the first two.
		t.ev('c', { name: 'claude-code' });
		expect([t.model.actorLabel('a'), t.model.actorLabel('b'), t.model.actorLabel('c')]).toEqual(['claude-code', 'claude-code 2', 'claude-code 3']);

		// Activity's actor for each one's operation, with the model's actor labels as the tab passes them.
		const a = op({ id: 'oa', state: 'completed', session: mcp('a', 'claude-code') });
		const b = op({ id: 'ob', state: 'completed', session: mcp('b', 'claude-code') });
		const ctx = { labels: t.model.actorLabels(), heldBuild: null, ownFailure: null };
		const blocks = buildBlocks([a, b], { actor: 'all', agentKey: null }, ctx);
		expect(blocks.map((x) => [x.id, x.actor])).toEqual([
			['ob', 'claude-code 2'],
			['oa', 'claude-code'],
		]);
		// B's filter excludes A's operations (keyed by the session, not by a label).
		expect(buildBlocks([a, b], { actor: 'all', agentKey: 'b' }, ctx).map((x) => x.id)).toEqual(['ob']);
	});

	// Decision: the name comes from the operations too, for a session the tab met through its
	// calls before the watch listed it, and "in use by" reads the actor label.
	it('words a held device by the actor label', async () => {
		const t = await watched();
		await t.snapshot([listed('a', 'claude-code'), { ...listed('b', 'claude-code'), devices: ['emulator-5554'] }]);
		t.ev('a', { ended: true });
		const lines = deviceHolds(t.model.view.rows, (id) => t.model.actorLabel(id)).map((h) => inUseText(h));
		expect(lines).toEqual(['in use by claude-code 2']);
	});
});

describe('SessionsModel with session.watch', () => {
	// Decision (ADR-0083 §1): a connected session is a row before it runs anything, and
	// several sit in one worktree. With operations only, both would be invisible.
	it('lists connected sessions that ran nothing, several in one workspace', async () => {
		const t = await watched();
		await t.snapshot([
			{ id: 's1', name: 'claude', workspace: 'wt', connectedAt: at },
			{ id: 's2', name: 'claude', workspace: 'wt', connectedAt: at },
			{ id: 's3', connectedAt: at },
		]);
		// Idle rows keep the existing order: by label.
		expect(t.model.view.rows.map((r) => [r.key, r.label, r.workspace, r.running])).toEqual([
			['s3', 'agent', null, false],
			['s1', 'claude', 'wt', false],
			['s2', 'claude 2', 'wt', false],
		]);
	});

	// Decision (ADR-0083 §1): the watch's workspace is the session's, and operations still give running.
	it('takes the workspace from the state event and keeps the operations for running', async () => {
		const t = await watched();
		await t.snapshot([{ id: 's1', name: 'a', workspace: 'main', connectedAt: at }]);
		t.src.set(op({ id: 'x', state: 'running', session: mcp('s1', 'a', 'main') }));
		t.ev('s1', { name: 'a', workspace: 'wt' });
		const row = t.model.view.rows[0]!;
		expect([row.workspace, row.running]).toEqual(['wt', true]);
	});

	// Decision (ADR-0083 §3): a selection made from a session's row follows its moves; one
	// that was not (never followed, or ended by a project click) does not.
	it('moves the selection of a followed session only', async () => {
		const t = await watched();
		await t.snapshot([{ id: 's1', name: 'a', workspace: 'main', connectedAt: at }]);
		t.ev('s1', { name: 'a', workspace: 'wt' });
		expect(t.moves).toEqual([]); // never followed: the UI never moves it
		t.model.follow('s1');
		t.ev('s1', { name: 'a', workspace: 'wt2' });
		t.ev('s1', { name: 'a', workspace: 'wt2' }); // not a move
		expect(t.moves).toEqual(['wt2']);
		t.model.unfollow(); // a click on a project or worktree row
		t.ev('s1', { name: 'a', workspace: 'main' });
		expect(t.moves).toEqual(['wt2']);
	});

	// Decision (ADR-0134): the panel marks the followed session, so follow and unfollow notify.
	it('notifies the listeners on follow and unfollow', async () => {
		const t = await watched();
		let n = 0;
		t.model.subscribe(() => n++);
		t.model.follow('s1');
		expect(n).toBe(1);
		t.model.unfollow();
		expect(n).toBe(2);
	});

	// Decision (ADR-0134): the follow's own navigation keeps it, any other route ends it. Without
	// `routed` a tab or an address change would leave a session followed that the user left.
	it('routed keeps the follow at the followed session workspace and ends it elsewhere', async () => {
		const t = await watched();
		await t.snapshot([{ id: 's1', name: 'a', workspace: 'wt', connectedAt: at }]);
		t.model.follow('s1');
		t.model.routed('wt'); // the follow's own navigation
		expect(t.model.following).toBe('s1');
		t.model.routed('other');
		expect(t.model.following).toBeNull();
		t.model.routed('wt'); // no follow: nothing happens
		expect(t.model.following).toBeNull();
		t.model.follow('s1');
		t.model.routed(null);
		expect(t.model.following).toBeNull();
	});

	// Decision (ADR-0127 §4): a session the watch reports ended leaves at once, running or not;
	// its operations stay in Activity, under its actor label. Keeping a running one until its
	// last operation ended would leave a row the watch no longer lists.
	it('drops an ended session at once, idle or running', async () => {
		const t = await watched();
		await t.snapshot([
			{ id: 'idle', name: 'i', workspace: 'w', connectedAt: at },
			{ id: 'busy', name: 'b', workspace: 'w', connectedAt: at },
		]);
		t.src.set(op({ id: 'r', state: 'running', session: mcp('busy', 'b', 'w') }));
		t.ev('idle', { ended: true });
		t.ev('busy', { ended: true });
		expect(t.model.view.rows).toEqual([]);
	});

	// A session absent from a new snapshot ended while the watch was blind (events.gap).
	it('reads the list again after a gap and drops a session no longer listed', async () => {
		const t = await watched();
		await t.snapshot([
			{ id: 's1', name: 'a', connectedAt: at },
			{ id: 's2', name: 'b', connectedAt: at },
		]);
		t.stream.emit(0, { topic: 'events.gap', type: 'gap', payload: {} });
		t.stream.answer(1, { subscriptionId: 'sw2', sessions: [{ id: 's2', name: 'b', connectedAt: at }] });
		await settle();
		expect(t.model.view.rows.map((r) => r.key)).toEqual(['s2']);
	});

	// A re-read is the truth: a session it lists is not ended, whatever an earlier read
	// concluded, and a followed session that moved while events were lost still moves the
	// selection (code review of step 8).
	it('a re-read lists a session again and moves the selection of a followed one', async () => {
		const t = await watched();
		await t.snapshot([
			{ id: 's1', name: 'a', workspace: 'main', connectedAt: at },
			{ id: 's2', name: 'b', workspace: 'main', connectedAt: at },
		]);
		t.model.follow('s1');
		t.stream.emit(0, { topic: 'events.gap', type: 'gap', payload: {} });
		t.stream.answer(1, { subscriptionId: 'sw2', sessions: [{ id: 's1', name: 'a', workspace: 'main', connectedAt: at }] });
		await settle();
		expect(t.model.view.rows.map((r) => r.key)).toEqual(['s1']);
		t.stream.emit(1, { topic: 'events.gap', type: 'gap', payload: {} });
		t.stream.answer(2, {
			subscriptionId: 'sw3',
			sessions: [
				{ id: 's1', name: 'a', workspace: 'wt', connectedAt: at },
				{ id: 's2', name: 'b', workspace: 'main', connectedAt: at },
			],
		});
		await settle();
		expect(t.model.view.rows.map((r) => r.key).sort()).toEqual(['s1', 's2']);
		expect(t.moves).toEqual(['wt']);
	});
});

// --- occupation (ADR-0094) ------------------------------------------------------------

describe('a session that occupies', () => {
	// Decision (ADR-0094 §3, §4): a row carries what its session holds and whether it is idle,
	// from the snapshot and the events alike; a session that holds nothing says so with an
	// empty list and no worktree, and idleness ends with the next event that does not carry it.
	it('shows occupies, devices and idle from the snapshot and the events', async () => {
		const t = await watched();
		await t.snapshot([
			{ id: 's1', name: 'claude', workspace: 'wt1', occupies: 'wt1', devices: ['emulator-5554'], stale: true, connectedAt: at },
			{ id: 's2', name: 'cursor', workspace: 'main', connectedAt: at },
		]);
		const by = () => new Map(t.model.view.rows.map((r) => [r.key, r]));
		expect(by().get('s1')).toMatchObject({ occupies: 'wt1', devices: ['emulator-5554'], idle: true });
		expect(by().get('s2')).toMatchObject({ occupies: null, devices: [], idle: false });
		// The owner's release: the event no longer carries occupies or stale, and the row follows.
		t.ev('s1', { name: 'claude', workspace: 'wt1' });
		expect(by().get('s1')).toMatchObject({ occupies: null, devices: [], idle: false, workspace: 'wt1' });
	});

	// Decision (ADR-0094 §3): the owner's release names the worktree by its workspace id or the
	// device by its serial, one of them, and nothing else: it never moves a session.
	it("releases with exactly the worktree's workspace id or the device's serial", async () => {
		const calls: [string, unknown][] = [];
		const client = {
			call: (n: string, p: unknown) => {
				calls.push([n, p]);
				return Promise.resolve({});
			},
			subscribe: () => new Promise(() => {}),
		} as unknown as SessionsClient;
		const model = new SessionsModel(new Source(), Date.now, { client });
		await model.release({ workspace: 'wt1-abc' });
		await model.release({ device: 'emulator-5554' });
		expect(calls).toEqual([
			['session.release', { workspace: 'wt1-abc' }],
			['session.release', { device: 'emulator-5554' }],
		]);
	});

	// Decision (ADR-0094 §3, §4): the devices panel's lines are the live rows' devices, so an
	// event that drops a session's devices (it ended, moved to the main tree, was released)
	// removes the line, and one that sets stale words it "(idle)". Read from device.list, which
	// is not re-read on those events, a line would outlive its hold.
	it('derives the held-device lines from the live rows', async () => {
		const t = await watched();
		await t.snapshot([
			{ id: 's1', name: 'claude', workspace: 'wt1', devices: ['emulator-5556', 'emulator-5554'], connectedAt: at },
			{ id: 's2', name: 'cursor', workspace: 'main', connectedAt: at },
		]);
		const lines = () => deviceHolds(t.model.view.rows, (id) => t.model.actorLabel(id)).map((h) => `${h.serial}: ${inUseText(h)}`);
		expect(lines()).toEqual(['emulator-5554: in use by claude', 'emulator-5556: in use by claude']);
		t.ev('s1', { name: 'claude', workspace: 'wt1', devices: ['emulator-5556', 'emulator-5554'], stale: true });
		expect(lines()).toEqual(['emulator-5554: in use by claude (idle)', 'emulator-5556: in use by claude (idle)']);
		t.ev('s1', { name: 'claude', workspace: 'wt1', devices: ['emulator-5556'] });
		expect(lines()).toEqual(['emulator-5556: in use by claude']);
		t.ev('s1', { ended: true });
		expect(lines()).toEqual([]);
	});

	// Decision (ADR-0094 §1, §4): the worktree row names its holder and says idle when it is,
	// and the session row names the worktree (by the panel's name for it) and the devices.
	it('words the holder of a worktree and what a session holds', () => {
		const row = (over: Partial<AgentRow>): AgentRow => ({
			key: 'k', label: 'claude', name: 'claude', workspace: null, running: false, result: null, unread: false, operations: [],
			occupies: null, devices: [], idle: false, ...over,
		});
		const rows = [row({ key: 'a', label: 'claude', occupies: 'wt1' }), row({ key: 'b', label: 'claude 2', occupies: 'wt2', idle: true })];
		expect(holderOf(rows, 'wt1')?.key).toBe('a');
		expect(heldByText('claude', false)).toBe('held by claude');
		expect(heldByText('claude 2', true)).toBe('held by claude 2 (idle)');
		expect(holderOf(rows, 'wt9')).toBeUndefined();
		const names = new Map([['wt1', 'task-one']]);
		expect(occupiesText(row({ occupies: 'wt1', devices: ['emulator-5554', 'emulator-5556'] }), (id) => names.get(id))).toBe('task-one, emulator-5554, emulator-5556');
		expect(occupiesText(row({ occupies: 'wt2' }), (id) => names.get(id))).toBe('wt2');
		expect(occupiesText(row({}), () => undefined)).toBe('');
	});
});

describe('duration', () => {
	it('reads seconds, minutes and hours', () => {
		expect(duration(12_400)).toBe('12 s');
		expect(duration(-5)).toBe('0 s');
		expect(duration(65_000)).toBe('1 m 05 s');
		expect(duration(3_720_000)).toBe('1 h 02 m');
	});
});
