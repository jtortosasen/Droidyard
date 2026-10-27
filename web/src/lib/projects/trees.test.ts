import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ConnectionError } from '../api/client';
import { POLL_MS, REFRESH_DELAY_MS } from './chats';
import type { ProjectRow } from './projects';
import type { AgentRow } from './sessions';
import { DESCRIBE_CAP, TreesModel, treeRows, type DescribedWorktree, type TreesClient } from './trees';

const project = (id: string, root: string, over: Partial<ProjectRow> = {}): ProjectRow =>
	({ id, name: root.split('/').pop()!, root, lastOpenedAt: '2026-10-10T10:00:00Z', state: 'ready', trusted: true, open: true, ...over }) as ProjectRow;

const live = (key: string, over: Partial<AgentRow> = {}): AgentRow => ({
	key,
	label: 'claude-code',
	name: 'claude-code',
	workspace: null,
	running: false,
	result: null,
	unread: false,
	operations: [],
	occupies: null,
	devices: [],
	idle: false,
	...over,
});

const wt = (root: string, over: Partial<DescribedWorktree> = {}): DescribedWorktree => ({ root, branch: 'feat', ...over });
const summary = (rows: ReturnType<typeof treeRows>) => rows.map((r) => [r.name, r.primary, r.workspace ?? null, r.open, r.gone]);

describe('treeRows', () => {
	const main = project('m', '/p/app');

	// Decision (ADR-0134 Q1, ADR-0089 §1): the main tree first and primary, then the linked ones
	// in root order, whether or not the daemon lists them; one not opened has no workspace.
	it('lists the main tree first as primary, then the linked worktrees in root order', () => {
		const rows = treeRows(main, [main], [wt('/p/app-b'), wt('/p/app-a')], [], new Map());
		expect(summary(rows)).toEqual([
			['app', true, 'm', true, false],
			['app-a', false, null, false, false],
			['app-b', false, null, false, false],
		]);
		expect(rows[1]).toMatchObject({ branch: 'feat', state: 'closed', trusted: false });
		expect(rows[0]!.branch).toBeUndefined();
	});

	// Decision (ADR-0089 §1): a listed worktree is matched to describe's by root, trailing
	// separators aside. A string compare would show it twice.
	it('merges a listed worktree with its describe entry by rootKey', () => {
		const w = project('w', '/p/app-a', { mainRoot: '/p/app' });
		const rows = treeRows(main, [main, w], [wt('/p/app-a/')], [], new Map());
		expect(summary(rows)).toEqual([
			['app', true, 'm', true, false],
			['app-a', false, 'w', true, false],
		]);
		expect(rows[1]!.branch).toBe('feat');
	});

	// Decision (ADR-0134): a listed worktree that is closed and gone from a loaded describe is
	// `gone`; an open one is not (it is in use); no describe says nothing about it.
	it('marks gone only a closed listed worktree absent from a loaded describe', () => {
		const closed = project('c', '/p/app-c', { mainRoot: '/p/app', open: false, state: 'closed' });
		const open = project('o', '/p/app-o', { mainRoot: '/p/app' });
		const all = [main, closed, open];
		expect(treeRows(main, all, [], [], new Map()).map((r) => [r.name, r.gone])).toEqual([
			['app', false],
			['app-c', true],
			['app-o', false],
		]);
		expect(treeRows(main, all, null, [], new Map()).some((r) => r.gone)).toBe(false);
		// A list at describe's cap may have left the worktree out: not gone.
		const full = Array.from({ length: DESCRIBE_CAP }, (_, i) => wt(`/p/other-${String(i).padStart(2, '0')}`));
		expect(treeRows(main, all, full, [], new Map()).some((r) => r.gone)).toBe(false);
	});

	// Decision (ADR-0134): a worktree workspace.watch lists shows before the next read, so
	// `described` null (not read, failed) still gives the listed rows. Describe as the only source
	// would show nothing until the read.
	it('shows the listed worktrees when described is null', () => {
		const w = project('w', '/p/app-new', { mainRoot: '/p/app' });
		expect(summary(treeRows(main, [main, w], null, [], new Map()))).toEqual([
			['app', true, 'm', true, false],
			['app-new', false, 'w', true, false],
		]);
	});

	// Decision (ADR-0134 Q2, ADR-0048 §Alternatives): a session bound in a worktree nests there with
	// the title of the chat it joined; an unclaimed one reads "<label> connected", and two with the
	// same name stay two (labels already numbered). The client's name as the fallback would read
	// the same for both and for a claimed one.
	it('nests sessions with their chat title, else "<label> connected", never merging two', () => {
		const w = project('w', '/p/app-a', { mainRoot: '/p/app' });
		const sessions = [
			live('s1', { workspace: 'w' }),
			live('s2', { workspace: 'w', name: 'cursor-vscode', label: 'cursor-vscode' }),
			live('s3', { workspace: 'm', name: 'Cursor', label: 'Cursor' }),
			live('s4', { workspace: 'm', name: 'Cursor', label: 'Cursor 2' }),
			live('s5', { workspace: null }),
		];
		const rows = treeRows(main, [main, w], [wt('/p/app-a')], sessions, new Map([['s1', 'Fix the crash']]));
		expect(rows[1]!.sessions.map((s) => [s.row.key, s.client, s.title])).toEqual([
			['s1', 'claude-code', 'Fix the crash'],
			['s2', 'cursor', 'cursor-vscode connected'],
		]);
		expect(rows[0]!.sessions.map((s) => s.title)).toEqual(['Cursor connected', 'Cursor 2 connected']);
	});

	// Decision (ADR-0134 Q3): `current` marks the tab's own row, from either a main or a worktree tab.
	it("marks the tab's row current, and the main tree is first from a worktree tab too", () => {
		const w = project('w', '/p/app-a', { mainRoot: '/p/app' });
		const fromMain = treeRows(main, [main, w], null, [], new Map());
		expect(fromMain.map((r) => r.current)).toEqual([true, false]);
		const fromWt = treeRows(w, [main, w], [wt('/p/app-a')], [], new Map());
		expect(fromWt.map((r) => [r.name, r.primary, r.current])).toEqual([
			['app', true, false],
			['app-a', false, true],
		]);
	});

	// Decision (ADR-0134): the main tree's tab is not listed (known only through its worktree): its
	// row stands for the root and is closed; no tab gives no rows.
	it('stands a main tree that is not listed on its root, and gives none without a tab', () => {
		const w = project('w', '/p/app-a', { mainRoot: '/p/app' });
		const rows = treeRows(w, [w], null, [], new Map());
		expect(summary(rows)).toEqual([
			['app', true, null, false, false],
			['app-a', false, 'w', true, false],
		]);
		expect(treeRows(null, [main], null, [], new Map())).toEqual([]);
	});

	// Decision (ADR-0094): a worktree's holder is a session of this panel, else the other chat
	// describe names.
	it('names the holder', () => {
		const w = project('w', '/p/app-a', { mainRoot: '/p/app' });
		const held = treeRows(main, [main, w], [wt('/p/app-a', { occupiedBy: { name: 'cursor', since: 'x', stale: true } })], [], new Map());
		expect(held[1]!.holder).toEqual({ label: 'cursor', idle: true });
		const mine = treeRows(main, [main, w], [wt('/p/app-a')], [live('s', { occupies: 'w', label: 'claude-code' })], new Map());
		expect(mine[1]!.holder).toEqual({ label: 'claude-code', idle: false });
		expect(mine[0]!.holder).toBeUndefined();
	});
});

describe('TreesModel', () => {
	beforeEach(() => vi.useFakeTimers());
	afterEach(() => vi.useRealTimers());

	const answer = (worktrees?: DescribedWorktree[]) => ({ id: 'x', name: 'x', root: '/p/app', state: 'ready', trusted: true, ...(worktrees ? { worktrees } : {}) });
	function setup(fn: (root: string | undefined) => Promise<unknown> = () => Promise.resolve(answer([wt('/p/app-a')]))) {
		const calls: [string, string | undefined][] = [];
		const client = {
			call: (name: string, _p: unknown, opts?: { workspace?: string }) => {
				calls.push([name, opts?.workspace]);
				return fn(opts?.workspace);
			},
		} as unknown as TreesClient;
		return { model: new TreesModel(client), calls };
	}

	it('reads the shown root and lists what describe says', async () => {
		const t = setup();
		t.model.show('/p/app');
		await vi.advanceTimersByTimeAsync(0);
		expect(t.calls).toEqual([['workspace.describe', '/p/app']]);
		expect(t.model.view.described).toEqual([wt('/p/app-a')]);
		t.model.show(null);
	});

	// Decision (ADR-0089 §3): no field is no worktree or more than 100 entries, so it says nothing
	// about a listed one; read as an empty list, every closed worktree would read gone.
	it('reads a describe with no worktrees field as nothing known', async () => {
		const t = setup(() => Promise.resolve(answer()));
		t.model.show('/p/app');
		await vi.advanceTimersByTimeAsync(0);
		expect(t.model.view.described).toBeNull();
		t.model.show(null);
	});

	// Decision (ADR-0134, as chats.ts): one read a second after the first change, whatever the
	// burst; a read per event would call three times.
	it('makes one read for a burst of three changed()', async () => {
		const t = setup();
		t.model.show('/p/app');
		await vi.advanceTimersByTimeAsync(0);
		t.model.changed();
		t.model.changed();
		await vi.advanceTimersByTimeAsync(REFRESH_DELAY_MS - 1);
		t.model.changed();
		expect(t.calls).toHaveLength(1);
		await vi.advanceTimersByTimeAsync(1);
		expect(t.calls).toHaveLength(2);
		await vi.advanceTimersByTimeAsync(REFRESH_DELAY_MS * 2);
		expect(t.calls).toHaveLength(2);
		t.model.show(null);
	});

	it('reads every 30 s while shown and not after', async () => {
		const t = setup();
		t.model.show('/p/app');
		await vi.advanceTimersByTimeAsync(POLL_MS * 2 + 5);
		expect(t.calls).toHaveLength(3);
		t.model.show(null);
		await vi.advanceTimersByTimeAsync(POLL_MS * 2);
		expect(t.calls).toHaveLength(3);
	});

	// Decision (ADR-0134): an answer for a root no longer shown is dropped; the worktrees of the
	// project before would otherwise be listed under the project now shown.
	it('drops an answer for a root no longer shown', async () => {
		let release: (v: unknown) => void = () => {};
		const t = setup((root) => (root === '/p/old' ? new Promise((r) => (release = r)) : Promise.resolve(answer([wt('/q/new-a')]))));
		t.model.show('/p/old');
		await vi.advanceTimersByTimeAsync(0);
		t.model.show('/q/new');
		await vi.advanceTimersByTimeAsync(0);
		release(answer([wt('/p/old-a')]));
		await vi.advanceTimersByTimeAsync(0);
		expect(t.model.view.described).toEqual([wt('/q/new-a')]);
		t.model.show(null);
	});

	// Decision (ADR-0134): a failed read keeps the last list and says why.
	it('keeps the rows and notes a failure', async () => {
		let fail = false;
		const t = setup(() => (fail ? Promise.reject(new Error('boom')) : Promise.resolve(answer([wt('/p/app-a')]))));
		t.model.show('/p/app');
		await vi.advanceTimersByTimeAsync(0);
		fail = true;
		await vi.advanceTimersByTimeAsync(POLL_MS);
		expect(t.model.view.described).toEqual([wt('/p/app-a')]);
		expect(t.model.view.error).not.toBeNull();
		t.model.show(null);
	});

	it('reads again when a lost connection opens', async () => {
		let lose = true;
		const t = setup(() => (lose ? Promise.reject(new ConnectionError('lost')) : Promise.resolve(answer([wt('/p/app-a')]))));
		t.model.show('/p/app');
		await vi.advanceTimersByTimeAsync(0);
		expect(t.model.view.described).toBeNull();
		lose = false;
		t.model.onConnection('open');
		await vi.advanceTimersByTimeAsync(0);
		expect(t.model.view.described).toEqual([wt('/p/app-a')]);
		t.model.show(null);
	});
});
