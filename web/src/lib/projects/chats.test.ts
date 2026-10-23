import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ConnectionError } from '../api/client';
import { agoText, chatTree, ChatsModel, clientOf, cut, POLL_MS, REFRESH_DELAY_MS, resumeCommand, sourceNotes, type Chat, type ChatsClient } from './chats';
import type { ProjectRow } from './projects';
import type { AgentRow } from './sessions';

const chat = (id: string, over: Partial<Chat> = {}): Chat => ({
	client: 'claude',
	store: 'claude-code',
	id,
	project: '/p/app',
	title: `title ${id}`,
	updatedAt: '2026-10-10T10:00:00Z',
	...over,
});

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

const project = (id: string, root: string, over: Partial<ProjectRow> = {}): ProjectRow =>
	({ id, name: root.split('/').pop()!, root, lastOpenedAt: '2026-10-10T10:00:00Z', state: 'ready', trusted: true, open: true, ...over }) as ProjectRow;

const flat = (tree: ReturnType<typeof chatTree>) => tree.map((c) => [c.name, c.projects.map((p) => [p.name, p.rows.map((r) => r.key)])]);

describe('chatTree: the order', () => {
	// Decision (ADR-0127 §4): clients in the order Claude Code, Cursor, then Other; projects by
	// their newest chat; chats newest first. A flat sort by name or id would fail all three.
	it('orders clients, then projects by newest chat, then chats newest first', () => {
		const tree = chatTree(
			[
				chat('old', { project: '/p/a', updatedAt: '2026-10-01T00:00:00Z' }),
				chat('new', { project: '/p/b', updatedAt: '2026-10-09T00:00:00Z' }),
				chat('mid', { project: '/p/b', updatedAt: '2026-10-05T00:00:00Z' }),
				chat('cur', { client: 'cursor', store: 'cursor-agent', project: '/p/a' }),
			],
			[live('peer', { name: 'laptop-link' })],
			[],
		);
		expect(flat(tree)).toEqual([
			[
				'Claude Code',
				[
					['b', ['claude-code:new', 'claude-code:mid']],
					['a', ['claude-code:old']],
				],
			],
			['Cursor', [['a', ['cursor-agent:cur']]]],
			['Other', [['No project', ['peer']]]],
		]);
	});
});

describe('chatTree: live sessions', () => {
	// Decision (ADR-0095 §1, ADR-0127 §4): a chat that names a live session takes that row's
	// state and no second row appears for the session.
	it('puts a live session on its chat row and adds no row of its own', () => {
		const s = live('s1', { running: true });
		const tree = chatTree([chat('c1', { session: 's1' })], [s], []);
		expect(flat(tree)).toEqual([['Claude Code', [['app', ['claude-code:c1']]]]]);
		expect(tree[0]!.projects[0]!.rows[0]!.live).toBe(s);
		expect(tree[0]!.projects[0]!.rows[0]!.title).toBe('title c1');
	});

	// Decision (ADR-0095 §1): Cursor's connection carries no chat id, so a live Cursor session
	// is a row of its own under Cursor and its workspace's project. `Cursor` as cursor-agent
	// says it, in either case.
	it('makes an unclaimed live Cursor session a row under Cursor and its project', () => {
		const tree = chatTree([chat('c1')], [live('cs', { name: 'Cursor', label: 'Cursor', workspace: 'w1' })], [project('w1', '/p/app')]);
		expect(flat(tree)).toEqual([
			['Claude Code', [['app', ['claude-code:c1']]]],
			['Cursor', [['app', ['cs']]]],
		]);
		expect(clientOf('Cursor')).toBe('cursor');
		expect(clientOf('cursor-vscode')).toBe('cursor');
		expect(clientOf('claude-code')).toBe('claude-code');
		expect(clientOf(null)).toBe('other');
	});

	// A worktree's session sits under the project of the main tree, not under the worktree.
	it("places a live session of a worktree under its main tree's project", () => {
		const rows = [project('m', '/p/app'), project('wt', '/p/app-wt', { mainRoot: '/p/app', name: 'app-wt' })];
		const tree = chatTree([], [live('s', { workspace: 'wt' })], rows);
		expect(tree[0]!.projects[0]).toMatchObject({ root: '/p/app', name: 'app' });
		expect(tree[0]!.projects[0]!.rows[0]).toMatchObject({ worktree: 'app-wt', workspace: 'wt' });
	});

	// Decision (ADR-0127 §4, ADR-0094 §3): a peer's link and a name the panel does not know sit
	// under Other with their holds and Release; the row is the session row itself.
	it('keeps the holds of a peer and an unknown name under Other', () => {
		const peer = live('p1', { name: 'laptop-link', label: 'laptop-link', occupies: 'wt1', devices: ['emulator-5554'] });
		const odd = live('o1', { name: null, label: 'agent' });
		const tree = chatTree([], [peer, odd], []);
		expect(tree.map((c) => c.name)).toEqual(['Other']);
		const rows = tree[0]!.projects[0]!.rows;
		expect(rows.map((r) => r.key).sort()).toEqual(['o1', 'p1']);
		expect(rows.find((r) => r.key === 'p1')!.live).toMatchObject({ occupies: 'wt1', devices: ['emulator-5554'] });
	});
});

describe('chatTree: titles', () => {
	// Decision (ADR-0127 §4): a chat without a title reads "Untitled · <date>", never its id or
	// anything else a chat holds. Falling back to the id would show a UUID where a name belongs.
	it('reads an untitled chat as Untitled with its date, whatever its other fields hold', () => {
		const c = chat('9f8e7d6c-1234', { title: undefined, worktree: '/p/app-wt', session: undefined });
		const r = chatTree([c, chat('blank', { title: '   ' })], [], [])[0]!.projects[0]!.rows;
		expect(r.map((x) => x.title)).toEqual(['Untitled · 2026-10-10', 'Untitled · 2026-10-10']);
		expect(r.map((x) => x.title).join()).not.toContain('9f8e7d6c');
	});

	// Decision (ADR-0015 §1): a title is data. It is carried verbatim, and it never reaches the
	// resume command.
	it('carries a title verbatim and keeps it out of the resume command', () => {
		const t = `x'; rm -rf / #`;
		const r = chatTree([chat('c1', { title: t })], [], [])[0]!.projects[0]!.rows[0]!;
		expect(r.title).toBe(t);
		expect(r.resume).toBe("cd '/p/app' && claude --resume c1");
		expect(r.resume).not.toContain('rm -rf');
	});
});

describe('chatTree: what a click selects', () => {
	// Decision (ADR-0127 §4): a chat row selects its worktree's workspace, else its project's,
	// and only an open one: it opens none.
	it("selects the worktree's workspace, else the project's, and never a closed one", () => {
		const rows = [project('m', '/p/app'), project('wt', '/p/app-wt', { mainRoot: '/p/app' }), project('gone', '/p/other', { open: false })];
		const r = (c: Chat) => chatTree([c], [], rows)[0]!.projects[0]!.rows[0]!.workspace;
		expect(r(chat('a', { worktree: '/p/app-wt' }))).toBe('wt');
		expect(r(chat('b'))).toBe('m');
		expect(r(chat('c', { project: '/p/other' }))).toBeNull();
		expect(r(chat('d', { project: '/p/unknown' }))).toBeNull();
	});
});

describe('resumeCommand', () => {
	// Decision (ADR-0127 §4): `cd '<dir>' && <client> --resume <id>`, the directory the worktree
	// else the project, a quote in it written '\''; none for the Cursor IDE.
	it('quotes a directory with a quote, and has none for the Cursor IDE', () => {
		const base = { id: 'abc', project: "/tmp/it's here" };
		expect(resumeCommand({ ...base, store: 'claude-code' })).toBe(`cd '/tmp/it'\\''s here' && claude --resume abc`);
		expect(resumeCommand({ ...base, store: 'cursor-agent', worktree: '/w t' })).toBe("cd '/w t' && cursor-agent --resume abc");
		expect(resumeCommand({ ...base, store: 'cursor-ide' })).toBeUndefined();
	});
});

describe('cut and notes', () => {
	// Decision (ADR-0127 §4): 8 per project and "N more"; opened shows all.
	it('shows 8 of 9 and one more, all once opened', () => {
		const rows = Array.from({ length: 9 }, (_, i) => i);
		expect(cut(rows, false)).toEqual({ shown: rows.slice(0, 8), more: 1 });
		expect(cut(rows, true)).toEqual({ shown: rows, more: 0 });
		expect(cut(rows.slice(0, 8), false).more).toBe(0);
	});

	it('notes a failed and an absent store, and no note for a read one', () => {
		expect(
			sourceNotes([
				{ store: 'claude-code', state: 'read' },
				{ store: 'cursor-agent', state: 'failed', cause: 'permission denied' },
				{ store: 'cursor-ide', state: 'absent' },
			]),
		).toEqual(['cursor-agent chats could not be read: permission denied', 'Cursor IDE: no chat store on this machine']);
	});

	it('words an age', () => {
		expect([agoText(5_000), agoText(5 * 60_000), agoText(3 * 3_600_000), agoText(2 * 86_400_000), agoText(-1)]).toEqual(['now', '5 m', '3 h', '2 d', 'now']);
	});
});

describe('ChatsModel', () => {
	beforeEach(() => vi.useFakeTimers());
	afterEach(() => vi.useRealTimers());

	const answer = (chats: Chat[] = []) => ({ chats, sources: [], truncated: false });
	function setup(fn: () => Promise<ReturnType<typeof answer>> = () => Promise.resolve(answer())) {
		const calls: unknown[] = [];
		const client: ChatsClient = {
			call: ((name: string, params: unknown) => {
				calls.push([name, params]);
				return fn();
			}) as ChatsClient['call'],
		};
		const model = new ChatsModel(client);
		return { model, calls };
	}

	// Decision (ADR-0127 §4): read on start, then a second after a session.state change, and a
	// burst is one call. A read per event would call three times.
	it('reads on start and once for a burst of three events', async () => {
		const t = setup();
		t.model.start();
		await vi.advanceTimersByTimeAsync(0);
		expect(t.calls).toHaveLength(1);
		t.model.changed();
		t.model.changed();
		await vi.advanceTimersByTimeAsync(REFRESH_DELAY_MS - 1);
		t.model.changed();
		expect(t.calls).toHaveLength(1);
		await vi.advanceTimersByTimeAsync(1);
		expect(t.calls).toHaveLength(2);
		await vi.advanceTimersByTimeAsync(REFRESH_DELAY_MS * 2);
		expect(t.calls).toHaveLength(2);
		t.model.stop();
	});

	// Decision (ADR-0127 §4): every 30 s while started, and not after stop.
	it('reads every 30 s while started', async () => {
		const t = setup();
		t.model.start();
		await vi.advanceTimersByTimeAsync(POLL_MS * 2 + 5);
		expect(t.calls).toHaveLength(3);
		t.model.stop();
		await vi.advanceTimersByTimeAsync(POLL_MS * 2);
		expect(t.calls).toHaveLength(3);
	});

	// Decision (ADR-0127 §4): a lost connection reads again once it is open.
	it('reads again once a lost connection opens', async () => {
		let fail = true;
		const t = setup(() => (fail ? Promise.reject(new ConnectionError('lost')) : Promise.resolve(answer([chat('c1')]))));
		t.model.start();
		await vi.advanceTimersByTimeAsync(0);
		expect(t.model.view.error).toBeNull(); // the connection state says it
		fail = false;
		t.model.onConnection('connecting');
		expect(t.calls).toHaveLength(1);
		t.model.onConnection('open');
		await vi.advanceTimersByTimeAsync(0);
		expect(t.calls).toHaveLength(2);
		expect(t.model.view.chats).toHaveLength(1);
		t.model.stop();
	});

	// Decision (ADR-0127 §4): a failure keeps the last list and a note.
	it('keeps the last list and notes a failure', async () => {
		let step = 0;
		const t = setup(() => (step++ === 0 ? Promise.resolve(answer([chat('c1')])) : Promise.reject(new Error('boom'))));
		t.model.start();
		await vi.advanceTimersByTimeAsync(0);
		expect(t.model.view).toMatchObject({ loaded: true, error: null });
		await vi.advanceTimersByTimeAsync(POLL_MS);
		expect(t.model.view.chats.map((c) => c.id)).toEqual(['c1']);
		expect(t.model.view.error?.message).toBe('boom');
		t.model.stop();
	});

	// One call at a time: a request made during a read is one more read after it, not a second in parallel.
	it('never runs two reads at once', async () => {
		let release: () => void = () => {};
		const t = setup(() => new Promise((r) => (release = () => r(answer()))));
		t.model.start();
		await vi.advanceTimersByTimeAsync(POLL_MS); // a poll while the first is out
		expect(t.calls).toHaveLength(1);
		release();
		await vi.advanceTimersByTimeAsync(0);
		expect(t.calls).toHaveLength(2);
		release();
		t.model.stop();
	});
});
