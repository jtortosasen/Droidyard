import { describe, expect, it } from 'vitest';
import { groupProjects, landing, ProjectsModel, sessionsByRoot, type ProjectRow, type ProjectsClient } from './projects';
import type { Route } from '../start/route';
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
	} as unknown as ProjectsClient;
	const navigated: string[] = [];
	const shown: true[] = [];
	const model = new ProjectsModel(client, (id) => navigated.push(id), () => shown.push(true));
	model.start();
	stream.answer(0, { subscriptionId: 'wa1', workspaces: rows, truncated: false });
	await settle();
	const ev = (id: string | null, payload: Record<string, unknown>, time = '2026-10-10T11:00:00Z') =>
		stream.emit(0, { topic: 'workspace.state', type: 'state', workspace: id, time, payload });
	const evType = (type: string, id: string | null) =>
		stream.emit(0, { topic: 'workspace.state', type, workspace: id, time: '2026-10-10T11:00:00Z', payload: {} });
	return { calls, stream, model, navigated, ev, evType, shown };
}

describe('groupProjects', () => {
	const r = (id: string, root: string, over: Record<string, unknown> = {}): ProjectRow => ({ ...row(id, { state: 'ready', root, name: id }), open: true, ...over }) as ProjectRow;
	const ses = (key: string, workspace: string | null) => ({ key, you: false, workspace });

	// Decision (ADR-0083 §2): worktrees nest under their main, a main whose tree is not listed
	// gets a group row, and a worktree is never a top-level row. Rows in one flat list would
	// show the worktrees as projects.
	it('nests worktrees under their main, and an orphan worktree under a group row', () => {
		const rows = [
			r('wt1', '/p/app-wt1', { mainRoot: '/p/app' }),
			r('app', '/p/app'),
			r('wt2', '/p/app-wt2', { mainRoot: '/p/app/' }),
			r('lone', '/q/lone-wt', { mainRoot: '/q/lone' }),
		];
		const { groups } = groupProjects(rows, []);
		expect(groups.map((g) => [g.root, g.project?.id ?? null, g.name, g.worktrees.map((w) => w.id)])).toEqual([
			['/p/app', 'app', 'app', ['wt1', 'wt2']],
			['/q/lone', null, 'lone', ['lone']],
		]);
	});

	// Decision (ADR-0083 §2): sessions name their worktree or "main", and a worktree with a
	// session is listed through it, not again among the worktrees without one.
	it('puts each session under its project naming its worktree, and lists only worktrees without one', () => {
		const rows = [r('app', '/p/app'), r('wt1', '/p/wt1', { mainRoot: '/p/app' }), r('wt2', '/p/wt2', { mainRoot: '/p/app' })];
		const { groups, loose } = groupProjects(rows, [ses('a', 'wt1'), ses('b', 'app'), ses('c', 'wt1'), ses('d', 'gone'), ses('e', null)]);
		expect(groups[0]!.sessions.map((s) => [s.session.key, s.where])).toEqual([
			['a', 'wt1'],
			['b', 'main'],
			['c', 'wt1'],
		]);
		expect(groups[0]!.worktrees.map((w) => w.id)).toEqual(['wt2']);
		expect(loose.map((s) => s.key)).toEqual(['d', 'e']);
	});

	// Decision (ADR-0083 §4): the Git panel names the sessions of a worktree by the root of their workspace.
	it('lists the sessions at each root for the Git panel, never You', () => {
		const rows = [r('app', '/p/app'), r('wt1', '/p/wt1', { mainRoot: '/p/app' })];
		const m = sessionsByRoot(rows, [
			{ ...ses('a', 'wt1'), label: 'claude' },
			{ ...ses('b', 'wt1'), label: 'codex' },
			{ ...ses('c', 'app'), label: 'main-one' },
			{ ...ses('y', 'wt1'), label: 'You', you: true },
		]);
		expect(m.get('/p/wt1')).toEqual(['claude', 'codex']);
		expect(m.get('/p/app')).toEqual(['main-one']);
	});
});

describe('ProjectsModel', () => {
	// Decision (ADR-0081): the row carries mainRoot, from the snapshot and from the event; without it nothing can nest.
	it('keeps mainRoot from the snapshot and from a state event', async () => {
		const t = await setup([row('wt', { mainRoot: '/r/app' })]);
		expect(t.model.view.rows[0]!.mainRoot).toBe('/r/app');
		t.ev('wt2', { state: 'ready', name: 'wt2', root: '/r/wt2', trusted: true, mainRoot: '/r/app' });
		expect(t.model.view.rows.find((x) => x.id === 'wt2')!.mainRoot).toBe('/r/app');
	});

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

	// Decision (ADR-0045 §3): the panel is live. A workspace an agent opens through MCP
	// after the snapshot reaches the list by its workspace.state event; a snapshot-only
	// panel would not show it.
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

	it('ends the watch when the page is left', async () => {
		const t = await setup([row('a')]);
		t.model.stop();
		expect(t.stream.stopped).toEqual(['wa1']);
		t.ev('x', { state: 'ready', name: 'n', root: '/r', trusted: false }); // a late event of the dead run
		expect(t.model.view.rows.map((r) => r.id)).toEqual(['a']);
	});

	// Decision (ADR-0047 §1): selecting a closed project opens it by its id and only then
	// routes to it; routing first would show a project the daemon has not opened.
	it('opens a closed row by its id, then routes to the id the daemon answers', async () => {
		const t = await setup([row('a')]);
		let routedBeforeOpen = false;
		t.calls.on('workspace.open', () => {
			routedBeforeOpen = t.navigated.length > 0;
			return { id: 'opened', name: 'n', root: '/x', state: 'ready', trusted: false };
		});
		await t.model.select('a');
		expect(t.calls.calls.find((c) => c.name === 'workspace.open')).toEqual({
			name: 'workspace.open',
			params: {},
			workspace: 'a',
		});
		expect(routedBeforeOpen).toBe(false);
		expect(t.navigated).toEqual(['opened']);
	});

	// Decision (ADR-0047 §1): an open project is shown at once; it needs no open call.
	it('routes to an open row without calling workspace.open', async () => {
		const t = await setup([row('a', { state: 'ready' })]);
		await t.model.select('a');
		expect(t.calls.names()).not.toContain('workspace.open');
		expect(t.navigated).toEqual(['a']);
	});

	it('stays put with the code when the open is refused', async () => {
		const t = await setup([row('a')]);
		t.calls.on('workspace.open', () => {
			throw capError('WORKSPACE_ROOT_INVALID');
		});
		await t.model.select('a');
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

describe('ProjectsModel: one tab, and a project removed', () => {
	// Decision (ADR-0084 §1): a show event selects its project as a click does: an open one
	// is shown at once, a closed one is opened by its id first, and the selection stays where
	// it is (onShow ends the following of a session). Treating it as a state event would
	// list a row with no name; ignoring it would leave the tab on the old project.
	it('selects the project a show event names, opening it if it is closed', async () => {
		const t = await setup([row('open', { state: 'ready' }), row('shut')]);
		t.evType('show', 'open');
		await settle();
		expect(t.navigated).toEqual(['open']);
		expect(t.shown).toHaveLength(1);
		t.evType('show', 'shut');
		await settle();
		expect(t.calls.calls.find((c) => c.name === 'workspace.open')?.workspace).toBe('shut');
		expect(t.navigated).toEqual(['open', 'opened']);
		expect(t.model.view.rows.map((r) => r.id).sort()).toEqual(['open', 'shut']); // no row was made of the event
	});

	// Decision (ADR-0086): only a closed row can be removed; the daemon refuses an open one,
	// so the model does not even ask. A removal that fails keeps the row and shows the code.
	it('removes a closed row, never an open one, and keeps the row when the call fails', async () => {
		const t = await setup([row('open', { state: 'ready' }), row('shut'), row('stuck')]);
		t.calls.on('workspace.forget', (p) => {
			if ((p as { workspace: string }).workspace === 'stuck') throw capError('WORKSPACE_STATE_UNREADABLE');
			return {};
		});
		await t.model.forget('open');
		expect(t.calls.names()).not.toContain('workspace.forget');
		await t.model.forget('shut');
		expect(t.calls.calls.find((c) => c.name === 'workspace.forget')).toMatchObject({ params: { workspace: 'shut' } });
		expect(t.model.view.rows.map((r) => r.id).sort()).toEqual(['open', 'stuck']);
		await t.model.forget('stuck');
		expect(t.model.view.rows.map((r) => r.id).sort()).toEqual(['open', 'stuck']);
		expect(t.model.view.error).toMatchObject({ code: 'WORKSPACE_STATE_UNREADABLE' });
	});

	// Decision (ADR-0086 §1): a forgotten event, from another tab or a CLI call, drops the
	// row here too.
	it('drops the row a forgotten event names', async () => {
		const t = await setup([row('a'), row('b')]);
		t.evType('forgotten', 'a');
		expect(t.model.view.rows.map((r) => r.id)).toEqual(['b']);
		t.evType('forgotten', 'unknown');
		expect(t.model.view.rows.map((r) => r.id)).toEqual(['b']);
	});
});

describe('landing', () => {
	const none: Route = { view: 'none' };
	const settled = async (rows: ReturnType<typeof row>[]) => (await setup(rows)).model.view;

	// Decision (ADR-0047 §2): without `w`, the newest open workspace.
	it('chooses the newest open project when the address names none', async () => {
		const view = await settled([
			row('closed-new', { lastOpenedAt: '2026-10-10T12:00:00Z' }),
			row('open-old', { state: 'ready', lastOpenedAt: '2026-10-01T12:00:00Z' }),
			row('open-new', { state: 'ready', lastOpenedAt: '2026-10-09T12:00:00Z' }),
		]);
		expect(landing(none, view, false)).toEqual({ kind: 'project', workspace: 'open-new' });
	});

	// Decision (ADR-0047 §2): none open pins the panel.
	it('pins the panel when no project is open', async () => {
		expect(landing(none, await settled([row('a')]), false)).toEqual({ kind: 'panel' });
		expect(landing(none, await settled([]), false)).toEqual({ kind: 'panel' });
	});

	// Decision (ADR-0047 §2): a `w` the daemon refused pins the panel with the reason, even
	// though another project is open: choosing that one would hide the reason.
	it('pins the panel for a refused w although a project is open', async () => {
		const view = await settled([row('a', { state: 'ready' })]);
		expect(landing(none, view, true)).toEqual({ kind: 'panel' });
	});

	it('waits for the list before choosing, and lets the address win', async () => {
		const t = await setup([row('a', { state: 'ready' })]);
		t.model.stop();
		expect(landing(none, t.model.view, false)).toEqual({ kind: 'waiting' });
		expect(landing({ view: 'project', workspace: 'x' }, t.model.view, true)).toEqual({ kind: 'project', workspace: 'x' });
	});
});
