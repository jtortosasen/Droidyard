import { describe, expect, it } from 'vitest';
import { PickerModel, type PickerClient } from './picker';
import { ConnectionError } from '../api/client';
import { capError, FakeCalls, FakeStream, settle } from '../build/testing';

const row = (id: string, over: Record<string, unknown> = {}) => ({
	id,
	name: `p-${id}`,
	root: `/r/${id}`,
	lastOpenedAt: '2026-10-10T10:00:00Z',
	state: 'closed',
	trusted: false,
	...over,
});

async function setup(rows: ReturnType<typeof row>[] = [], chooser: boolean | 'fails' = true) {
	const calls = new FakeCalls();
	const stream = new FakeStream('wa');
	calls.on('system.capabilities', () => {
		if (chooser === 'fails') throw new ConnectionError('lost');
		return {
			version: 'v',
			capabilities: [
				{ name: 'workspace.chooseRoot', available: chooser, ...(chooser ? {} : { unavailableReason: 'no osascript' }) },
			],
		};
	});
	calls.on('workspace.open', (_p) => ({ id: 'opened', name: 'n', root: '/x', state: 'ready', trusted: false }));
	const client = {
		call: (n: string, p: unknown, o?: { workspace?: string }) =>
			n === 'events.unsubscribe'
				? stream.call(n as 'events.unsubscribe', p as { subscriptionId: string })
				: (calls.call as (n: string, p: unknown, o?: unknown) => Promise<never>)(n, p, o),
		subscribe: (n: string, p: unknown, h: never) => stream.subscribe(n, p, h),
	} as unknown as PickerClient;
	const navigated: string[] = [];
	const model = new PickerModel(client, (id) => navigated.push(id));
	model.start();
	stream.answer(0, { subscriptionId: 'wa1', workspaces: rows, truncated: false });
	await settle();
	const ev = (id: string | null, payload: Record<string, unknown>, time = '2026-10-10T11:00:00Z') =>
		stream.emit(0, { topic: 'workspace.state', type: 'state', workspace: id, time, payload });
	return { calls, stream, model, navigated, ev };
}

describe('PickerModel', () => {
	it('lists the snapshot newest first, with the open ones marked', async () => {
		const t = await setup([
			row('old', { lastOpenedAt: '2026-10-01T10:00:00Z' }),
			row('new', { lastOpenedAt: '2026-10-09T10:00:00Z', state: 'ready', trusted: true }),
		]);
		expect(t.model.view.rows.map((r) => [r.id, r.open, r.trusted])).toEqual([
			['new', true, true],
			['old', false, false],
		]);
		expect(t.model.view.watch.state).toBe('watching');
	});

	// Decision (ADR-0045 §3): the picker is live. A workspace an agent opens through MCP
	// after the snapshot reaches the list by its workspace.state event; a snapshot-only
	// picker would not show it.
	it('merges the ready event of a workspace opened after the snapshot', async () => {
		const t = await setup([row('a')]);
		t.ev('mcp1', { state: 'ready', name: 'viaMcp', root: '/r/mcp', trusted: false });
		expect(t.model.view.rows.find((r) => r.id === 'mcp1')).toMatchObject({
			name: 'viaMcp',
			root: '/r/mcp',
			state: 'ready',
			open: true,
			lastOpenedAt: '2026-10-10T11:00:00Z',
		});
		expect(t.model.view.rows[0]!.id).toBe('mcp1'); // newest first
	});

	it('updates a known row by id and shows a closed one as not open', async () => {
		const t = await setup([row('a', { state: 'ready' })]);
		t.ev('a', { state: 'closed', name: 'p-a', root: '/r/a', trusted: false });
		expect(t.model.view.rows).toHaveLength(1);
		expect(t.model.view.rows[0]).toMatchObject({ id: 'a', state: 'closed', open: false });
		// Closing is not an open: the index's last-opened time is kept.
		expect(t.model.view.rows[0]!.lastOpenedAt).toBe('2026-10-10T10:00:00Z');
	});

	// Decision (ADR-0045 §3): "call this again after an events.gap".
	it('reads the list again after an events.gap and after a reconnect', async () => {
		const t = await setup([row('a')]);
		t.stream.emit(0, { topic: 'events.gap', type: 'gap', payload: {} });
		expect(t.stream.handlers).toHaveLength(2);
		expect(t.stream.stopped).toEqual(['wa1']);
		t.stream.answer(1, { subscriptionId: 'wa2', workspaces: [row('b')], truncated: false });
		await settle();
		expect(t.model.view.rows.map((r) => r.id)).toEqual(['b']);

		t.stream.handlers[1]!.onEnd!(new ConnectionError('lost'));
		expect(t.model.view.watch.state).toBe('waiting');
		t.model.onConnection('open');
		expect(t.stream.handlers).toHaveLength(3);
	});

	it('ends the watch when the picker is left', async () => {
		const t = await setup([row('a')]);
		t.model.stop();
		expect(t.stream.stopped).toEqual(['wa1']);
		t.ev('x', { state: 'ready', name: 'n', root: '/r', trusted: false }); // a late event of the dead run
		expect(t.model.view.rows.map((r) => r.id)).toEqual(['a']);
	});

	// Decision (ADR-0045 §2): a click opens the workspace by its id and the tab goes to #w=<id>.
	it('opens a clicked row by its id and navigates to the id the daemon answers', async () => {
		const t = await setup([row('a')]);
		await t.model.openRow('a');
		expect(t.calls.calls.find((c) => c.name === 'workspace.open')).toEqual({
			name: 'workspace.open',
			params: {},
			workspace: 'a',
		});
		expect(t.navigated).toEqual(['opened']);
	});

	it('stays on the picker with the code when the open is refused', async () => {
		const t = await setup([row('a')]);
		t.calls.on('workspace.open', () => {
			throw capError('WORKSPACE_ROOT_INVALID');
		});
		await t.model.openRow('a');
		expect(t.navigated).toEqual([]);
		expect(t.model.view.error).toMatchObject({ code: 'WORKSPACE_ROOT_INVALID' });
		expect(t.model.view.opening).toBe(false);
	});

	// Decision (ADR-0045 §4): the dialog's root is opened; a dismissed dialog is no choice
	// and opens nothing.
	it('opens the root the folder dialog answers with', async () => {
		const t = await setup();
		t.calls.on('workspace.chooseRoot', () => ({ root: '/chosen' }));
		await t.model.chooseFolder();
		expect(t.calls.calls.find((c) => c.name === 'workspace.open')?.workspace).toBe('/chosen');
		expect(t.navigated).toEqual(['opened']);
	});

	it('opens nothing when the dialog is dismissed', async () => {
		const t = await setup();
		t.calls.on('workspace.chooseRoot', () => ({ dismissed: true }));
		await t.model.chooseFolder();
		expect(t.calls.names()).not.toContain('workspace.open');
		expect(t.navigated).toEqual([]);
		expect(t.model.view.choosing).toBe(false);
		expect(t.model.view.error).toBeNull();
	});

	// Dismissed wins even if an answer carried a root as well (the contract says absent).
	it('opens nothing when dismissed is set, whatever else the answer holds', async () => {
		const t = await setup();
		t.calls.on('workspace.chooseRoot', () => ({ dismissed: true, root: '/stale' }));
		await t.model.chooseFolder();
		expect(t.calls.names()).not.toContain('workspace.open');
	});

	it('shows WORKSPACE_DIALOG_FAILED and opens nothing', async () => {
		const t = await setup();
		t.calls.on('workspace.chooseRoot', () => {
			throw capError('WORKSPACE_DIALOG_FAILED');
		});
		await t.model.chooseFolder();
		expect(t.model.view.error).toMatchObject({ code: 'WORKSPACE_DIALOG_FAILED' });
		expect(t.calls.names()).not.toContain('workspace.open');
	});

	// Decision (ADR-0045 §4): where the dialog is unavailable the UI falls back to the typed
	// root. The view says which, from system.capabilities' availability.
	it('reports the dialog available or unavailable from system.capabilities', async () => {
		expect((await setup([], true)).model.view.chooser).toBe('available');
		const off = await setup([], false);
		expect(off.model.view).toMatchObject({ chooser: 'unavailable', chooserReason: 'no osascript' });
	});

	it('keeps the dialog unknown, and the typed field usable, when system.capabilities fails', async () => {
		const t = await setup([], 'fails');
		expect(t.model.view.chooser).toBe('unknown');
		await t.model.openRoot('/typed');
		expect(t.navigated).toEqual(['opened']);
	});

	it('opens a typed root as typed, trimmed', async () => {
		const t = await setup();
		await t.model.openRoot('  /typed/root ');
		expect(t.calls.calls.find((c) => c.name === 'workspace.open')?.workspace).toBe('/typed/root');
		await t.model.openRoot('   ');
		expect(t.calls.calls.filter((c) => c.name === 'workspace.open')).toHaveLength(1);
	});
});
