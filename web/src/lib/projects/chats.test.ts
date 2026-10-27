import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ConnectionError } from '../api/client';
import { agoText, chatFolders, ChatsModel, clientOf, cut, POLL_MS, REFRESH_DELAY_MS, resumeCommand, sourceNotes, titlesBySession, type Chat, type ChatsClient } from './chats';
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

const folderKeys = (fs: ReturnType<typeof chatFolders>) => fs.map((f) => [f.name, f.rows.map((r) => r.key), f.connections.map((c) => c.row.key)]);

describe('chatFolders: the order', () => {
	// Decision (ADR-0134 Q1): the tab's project first whatever its age, then by newest chat,
	// "No project" last. A newest-first sort alone would put /p/old after /p/b.
	it("puts the tab's folder first whatever its age, then newest first, No project last", () => {
		const folders = chatFolders(
			[
				chat('old', { project: '/p/old', updatedAt: '2026-10-01T00:00:00Z' }),
				chat('new', { project: '/p/b', updatedAt: '2026-10-09T00:00:00Z' }),
				chat('mid', { project: '/p/b', updatedAt: '2026-10-05T00:00:00Z' }),
				chat('cur', { client: 'cursor', store: 'cursor-agent', project: '/p/c', updatedAt: '2026-10-03T00:00:00Z' }),
			],
			[live('peer', { name: 'laptop-link', label: 'laptop-link' })],
			[],
			'/p/old/',
		);
		expect(folderKeys(folders)).toEqual([
			['old', ['claude-code:old'], []],
			['b', ['claude-code:new', 'claude-code:mid'], []],
			['c', ['cursor-agent:cur'], []],
			['No project', [], ['peer']],
		]);
		expect(folders[0]!.current).toBe(true);
	});
});

describe('chatFolders: tags and connections', () => {
	// Decision (ADR-0134 Q2): the client is a tag taken from the chat's store; the Cursor IDE
	// and cursor-agent stay apart.
	it('tags each row with its store', () => {
		const folders = chatFolders(
			[chat('a'), chat('b', { client: 'cursor', store: 'cursor-agent' }), chat('c', { client: 'cursor', store: 'cursor-ide' })],
			[],
			[],
			null,
		);
		expect(Object.fromEntries(folders[0]!.rows.map((r) => [r.key, r.tag]))).toEqual({
			'claude-code:a': 'claude-code',
			'cursor-agent:b': 'cursor-agent',
			'cursor-ide:c': 'cursor-ide',
		});
	});

	// Decision (ADR-0134 Q1): a live session no chat claims is no row, and Cursor's connection
	// carries no chat id so it is never claimed. Today's tree made it a row.
	it('lists an unclaimed Cursor session as a connection, not a row', () => {
		const cs = live('cs', { name: 'Cursor', label: 'Cursor', workspace: 'w1' });
		const f = chatFolders([chat('c1', { client: 'cursor', store: 'cursor-agent' })], [cs], [project('w1', '/p/app')], null)[0]!;
		expect(f.rows.map((r) => r.key)).toEqual(['cursor-agent:c1']);
		expect(f.connections).toEqual([{ client: 'cursor', row: cs }]);
		expect(clientOf('Cursor')).toBe('cursor');
		expect(clientOf('cursor-vscode')).toBe('cursor');
		expect(clientOf('claude-code')).toBe('claude-code');
		expect(clientOf(null)).toBe('other');
	});

	// Decision (ADR-0095 §1): a chat that names a live session takes it and the session makes no
	// connection of its own.
	it('puts a claimed session on its chat row and not among the connections', () => {
		const s = live('s1', { running: true });
		const f = chatFolders([chat('c1', { session: 's1' })], [s], [], null)[0]!;
		expect(f.rows[0]!.live).toBe(s);
		expect(f.rows[0]!.presence).toBe('connected');
		expect(f.connections).toEqual([]);
	});

	// Decision (ADR-0134 Q2): the tab's folder has no connections (they nest on its worktrees);
	// another project's has them. A project with a connection and no chat still has a folder.
	it("keeps the tab's connections out of its folder and gives a chatless project a folder", () => {
		const rows = [project('a', '/p/app'), project('b', '/p/other')];
		const sessions = [live('sa', { workspace: 'a' }), live('sb', { workspace: 'b' })];
		const folders = chatFolders([chat('c1', { project: '/p/app' })], sessions, rows, '/p/app');
		expect(folderKeys(folders)).toEqual([
			['app', ['claude-code:c1'], []],
			['other', [], ['sb']],
		]);
	});

	// Decision (ADR-0134 Q2): a session with no listed workspace (a peer's link, an unbound MCP
	// session, one holding only a device) is a connection of "No project". A session of a
	// worktree is its main tree's connection.
	it('sends a session with no listed workspace to No project and a worktree one to its main tree', () => {
		const rows = [project('m', '/p/app'), project('wt', '/p/app-wt', { mainRoot: '/p/app' })];
		const sessions = [
			live('none', { workspace: null }),
			live('dev', { workspace: null, devices: ['emulator-5554'] }),
			live('ghost', { workspace: 'unlisted' }),
			live('inwt', { workspace: 'wt' }),
		];
		const folders = chatFolders([], sessions, rows, null);
		expect(folderKeys(folders)).toEqual([
			['app', [], ['inwt']],
			['No project', [], ['none', 'dev', 'ghost']],
		]);
	});
});

describe('chatFolders: presence', () => {
	const rows = [project('w1', '/p/app')];
	const presence = (cs: Chat[], ss: AgentRow[], tab: string | null = null) => chatFolders(cs, ss, rows, tab)[0]!.rows.map((r) => r.presence);

	// Decision (ADR-0134 Q2): joined is connected; a Claude Code chat nobody names is not
	// connected; the join is never guessed for Cursor, so an unclaimed connection of its client
	// beside it makes it unknown, and no such connection makes it not connected.
	it('reads connected, not-connected and unknown', () => {
		expect(presence([chat('a', { session: 's1' })], [live('s1')])).toEqual(['connected']);
		expect(presence([chat('a')], [])).toEqual(['not-connected']);
		const cursorChat = chat('c', { client: 'cursor', store: 'cursor-ide' });
		const vscode = live('cv', { name: 'cursor-vscode', label: 'cursor-vscode', workspace: 'w1' });
		expect(presence([cursorChat], [vscode])).toEqual(['unknown']);
		expect(presence([cursorChat], [])).toEqual(['not-connected']);
		expect(presence([cursorChat], [live('cl', { workspace: 'w1' })])).toEqual(['not-connected']); // a Claude Code session is not Cursor
		expect(presence([chat('a')], [live('unc', { workspace: 'w1' })])).toEqual(['unknown']);
	});

	// The tab's folder lists no connections, yet its chats still read them: unknown there too.
	it("counts the tab's unclaimed connections for presence though its folder lists none", () => {
		const f = chatFolders([chat('a')], [live('unc', { workspace: 'w1' })], rows, '/p/app')[0]!;
		expect(f.connections).toEqual([]);
		expect(f.rows[0]!.presence).toBe('unknown');
	});

	// An unclaimed session in another project says nothing about this folder's chats.
	it('does not read a connection of another project', () => {
		const two = [project('w1', '/p/app'), project('w2', '/p/other')];
		const f = chatFolders([chat('a')], [live('unc', { workspace: 'w2' })], two, null).find((x) => x.name === 'app')!;
		expect(f.rows[0]!.presence).toBe('not-connected');
	});
});

describe('titlesBySession', () => {
	it('maps a session id to the title of the chat that names it', () => {
		const m = titlesBySession([chat('a', { session: 's1' }), chat('b', { title: undefined, session: 's2' }), chat('c')]);
		expect([...m]).toEqual([
			['s1', 'title a'],
			['s2', 'Untitled · 2026-10-10'],
		]);
	});
});

describe('chatFolders: titles', () => {
	// Decision (ADR-0127 §4): a chat without a title reads "Untitled · <date>", never its id or
	// anything else a chat holds. Falling back to the id would show a UUID where a name belongs.
	it('reads an untitled chat as Untitled with its date, whatever its other fields hold', () => {
		const c = chat('9f8e7d6c-1234', { title: undefined, worktree: '/p/app-wt', session: undefined });
		const r = chatFolders([c, chat('blank', { title: '   ' })], [], [], null)[0]!.rows;
		expect(r.map((x) => x.title)).toEqual(['Untitled · 2026-10-10', 'Untitled · 2026-10-10']);
		expect(r.map((x) => x.title).join()).not.toContain('9f8e7d6c');
	});

	// Decision (ADR-0015 §1): a title is data. It is carried verbatim, and it never reaches the
	// resume command.
	it('carries a title verbatim and keeps it out of the resume command', () => {
		const t = `x'; rm -rf / #`;
		const r = chatFolders([chat('c1', { title: t })], [], [], null)[0]!.rows[0]!;
		expect(r.title).toBe(t);
		expect(r.resume).toBe("cd '/p/app' && claude --resume c1");
		expect(r.resume).not.toContain('rm -rf');
	});
});

describe('chatFolders: what a click selects', () => {
	// Decision (ADR-0127 §4): a chat row selects its worktree's workspace, else its project's,
	// and only an open one: it opens none.
	it("selects the worktree's workspace, else the project's, and never a closed one", () => {
		const rows = [project('m', '/p/app'), project('wt', '/p/app-wt', { mainRoot: '/p/app' }), project('gone', '/p/other', { open: false })];
		const r = (c: Chat) => chatFolders([c], [], rows, null)[0]!.rows[0]!.workspace;
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
