import { describe, expect, it } from 'vitest';
import type { Operation, OperationState } from '../ops/activity';
import { AGENT_ROW_TTL_MS, duration, SessionsModel, YOU, type SessionsSource } from './sessions';

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

function setup() {
	const clock = { t: Date.parse('2026-10-10T10:00:00Z') };
	const src = new Source();
	const model = new SessionsModel(src, () => clock.t);
	model.start();
	return { src, model, clock };
}

describe('SessionsModel', () => {
	// Decision (D30): MCP sessions are rows, labelled by name; the same name gets " 2" in the
	// order seen. Keyed by name alone, two sessions would merge into one row.
	it('shows two MCP sessions as two rows, a repeated name numbered in the order seen', () => {
		const t = setup();
		t.src.set(op({ id: 'a', state: 'running', session: mcp('s1', 'claude') }));
		t.src.set(op({ id: 'b', state: 'running', session: mcp('s2', 'claude') }));
		t.src.set(op({ id: 'c', state: 'running', session: mcp('s3', undefined) }));
		const rows = t.model.view.rows;
		expect(rows).toHaveLength(3);
		expect(new Map(rows.map((r) => [r.key, r.label]))).toEqual(
			new Map([
				['s1', 'claude'],
				['s2', 'claude 2'],
				['s3', 'agent'],
			]),
		);
	});

	// Decision (D30): `ui` and `cli` commands are one "You" row, and the tab's own reads are
	// not in it. A read-only capability from the UI would otherwise keep "You" running.
	it('folds ui and cli commands into one You row, without reads', () => {
		const t = setup();
		t.src.set(op({ id: 'a', state: 'running', client: 'ui', capability: 'build.assemble' }));
		t.src.set(op({ id: 'b', state: 'running', client: 'cli', capability: 'device.install' }));
		t.src.set(op({ id: 'r', state: 'running', client: 'ui', capability: 'device.list' }));
		const rows = t.model.view.rows;
		expect(rows.map((r) => [r.key, r.label, r.you])).toEqual([[YOU, 'You', true]]);
		expect(rows[0]!.operations.map((o) => o.id).sort()).toEqual(['a', 'b']);
	});

	// Decision (D30): a global operation sits under its session.workspace, and a session
	// without one in "No project" (workspace null).
	it('places a row under its session workspace, else under no project', () => {
		const t = setup();
		t.src.set(op({ id: 'a', state: 'running', workspace: null, session: mcp('s1', 'a', 'wA') }));
		t.src.set(op({ id: 'b', state: 'running', workspace: null, session: mcp('s2', 'b') }));
		const by = Object.fromEntries(t.model.view.rows.map((r) => [r.key, r.workspace]));
		expect(by).toEqual({ s1: 'wA', s2: null });
	});

	// Rests on (plan 7): a session's workspace is the latest event that carries one.
	it('takes the workspace from the latest operation that carries one', () => {
		const t = setup();
		t.src.set(op({ id: 'old', state: 'completed', session: mcp('s1', 'a', 'wOld') }));
		t.src.set(op({ id: 'mid', state: 'completed', session: mcp('s1', 'a', 'wNew') }));
		t.src.set(op({ id: 'new', state: 'running', session: mcp('s1', 'a') }));
		expect(t.model.view.rows[0]!.workspace).toBe('wNew');
	});

	// The bound workspace.open runs before its workspace is open, so it carries no
	// session.workspace (and the capability is global): it is connecting, not work, and a
	// session that has only opened its project is not a row rather than one in "No project".
	it('lists no row for a session whose only call is opening its project', () => {
		const t = setup();
		t.src.set(op({ id: 'o', state: 'completed', capability: 'workspace.open', workspace: null, session: mcp('s1', undefined) }));
		t.src.set(op({ id: 'u', state: 'completed', capability: 'workspace.open', workspace: null, client: 'ui' }));
		expect(t.model.view.rows).toEqual([]);
		t.src.set(op({ id: 'b', state: 'running', session: mcp('s1', 'agent-b', 'wB') }));
		expect(t.model.view.rows.map((r) => [r.label, r.workspace, r.unread, r.result])).toEqual([['agent-b', 'wB', false, null]]);
	});

	// The tab's own subscriptions (streaming watches) are not activity, as reads are not.
	it('leaves the tab own watches out of You', () => {
		const t = setup();
		t.src.set(op({ id: 'w', state: 'completed', client: 'ui', capability: 'file.watch' }));
		t.src.set(op({ id: 'x', state: 'completed', client: 'ui', capability: 'operation.watch' }));
		expect(t.model.view.rows).toEqual([]);
	});

	// Decision (D30): running rows first; a row runs while any of its operations is not terminal.
	it('lists running rows first and keeps a row running while any operation is live', () => {
		const t = setup();
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
	it('marks unread on an unselected row and clears it on select', () => {
		const t = setup();
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

	// Decision (D26): a row leaves 30 minutes after its last terminal event, not before, and
	// never while an operation runs.
	it('drops a row 30 minutes after its last terminal event', () => {
		const t = setup();
		t.src.set(op({ id: 'a', state: 'running', session: mcp('s1', 'a') }));
		t.clock.t += 40 * 60_000;
		expect(t.model.view.rows).toHaveLength(1); // running for 40 minutes: still there
		t.src.set(op({ id: 'a', state: 'completed', session: mcp('s1', 'a') }));
		t.clock.t += AGENT_ROW_TTL_MS - 1;
		expect(t.model.view.rows).toHaveLength(1);
		t.clock.t += 1;
		expect(t.model.view.rows).toHaveLength(0);
	});

	// Decision (D30): the rail's count and spinner follow agent rows only.
	it('counts running agents without You', () => {
		const t = setup();
		t.src.set(op({ id: 'u', state: 'running', client: 'ui' }));
		expect(t.model.view.runningAgents).toBe(0);
		t.src.set(op({ id: 'a', state: 'running', session: mcp('s1', 'a') }));
		t.src.set(op({ id: 'b', state: 'running', session: mcp('s2', 'b') }));
		expect(t.model.view.runningAgents).toBe(2);
	});

	it('lists a running operation with its capability, oldest first', () => {
		const t = setup();
		t.src.set(op({ id: 'a', state: 'running', capability: 'build.assemble', startedAt: '2026-10-10T10:00:00Z', session: mcp('s1', 'a') }));
		t.src.set(op({ id: 'b', state: 'running', capability: 'device.install', startedAt: '2026-10-10T10:00:05Z', session: mcp('s1', 'a') }));
		expect(t.model.view.rows[0]!.operations.map((o) => o.capability)).toEqual(['build.assemble', 'device.install']);
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
