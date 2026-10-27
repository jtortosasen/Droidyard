// The chats of the user's assistant clients and the Sessions folders built from them (ADR-0127
// §Decision 4 as superseded in part by ADR-0134). `session.chats` is a stateless read of the clients' own local stores, so the
// model reads it on start, one second after a `session.state` change (a burst is one call),
// every 30 seconds while started, and once more when a lost connection opens again. A failed
// read keeps the last list and a note.
//
// Every title is untrusted text a person or a model wrote (ADR-0015 §1): the tree carries it as
// data and the panel shows it as a text node; it never reaches `resumeCommand`.
import type { Params, Result } from '../api/capabilities.gen';
import { ConnectionError, type ConnectionState } from '../api/client';
import { failure, type Failure } from '../build/failure';
import { rootKey, type ProjectRow } from './projects';
import type { AgentRow } from './sessions';

/** Working defaults (ADR-0127 §Decision 4). */
export const REFRESH_DELAY_MS = 1000;
export const POLL_MS = 30_000;

export type Chat = Result<'session.chats'>['chats'][number];
export type ChatSource = Result<'session.chats'>['sources'][number];

export interface ChatsClient {
	call(name: 'session.chats', params: Params<'session.chats'>): Promise<Result<'session.chats'>>;
}

export interface ChatsView {
	chats: readonly Chat[];
	sources: readonly ChatSource[];
	truncated: boolean;
	/** The last read failed: the list above is the one before it. */
	error: Failure | null;
	/** A read answered at least once. */
	loaded: boolean;
}

export class ChatsModel {
	#client: ChatsClient;
	#view: ChatsView = { chats: [], sources: [], truncated: false, error: null, loaded: false };
	#started = false;
	#timer: ReturnType<typeof setInterval> | null = null;
	#soon: ReturnType<typeof setTimeout> | null = null;
	#inflight = false;
	#again = false;
	#waiting = false;
	#listeners = new Set<() => void>();

	constructor(client: ChatsClient) {
		this.#client = client;
	}

	get view(): ChatsView {
		return this.#view;
	}

	subscribe(listener: () => void): () => void {
		this.#listeners.add(listener);
		return () => {
			this.#listeners.delete(listener);
		};
	}

	start(): void {
		if (this.#started) return;
		this.#started = true;
		void this.#read();
		this.#timer = setInterval(() => void this.#read(), POLL_MS);
	}

	stop(): void {
		this.#started = false;
		this.#waiting = false;
		if (this.#timer !== null) clearInterval(this.#timer);
		if (this.#soon !== null) clearTimeout(this.#soon);
		this.#timer = null;
		this.#soon = null;
	}

	/** A `session.state` event arrived: read again in a second; events inside that second share the read. */
	changed(): void {
		if (!this.#started || this.#soon !== null) return;
		this.#soon = setTimeout(() => {
			this.#soon = null;
			void this.#read();
		}, REFRESH_DELAY_MS);
	}

	/** A lost connection reads again once it is open. */
	onConnection(state: ConnectionState): void {
		if (state === 'open' && this.#waiting && this.#started) {
			this.#waiting = false;
			void this.#read();
		}
	}

	async #read(): Promise<void> {
		// One call at a time: a request made during a call is one more read after it.
		if (this.#inflight) {
			this.#again = true;
			return;
		}
		this.#inflight = true;
		try {
			const r = await this.#client.call('session.chats', {});
			if (!this.#started) return;
			this.#view = { chats: r.chats, sources: r.sources, truncated: r.truncated, error: null, loaded: true };
			this.#waiting = false;
		} catch (e) {
			if (!this.#started) return;
			if (e instanceof ConnectionError && e.kind === 'lost') this.#waiting = true;
			else this.#view = { ...this.#view, error: failure(e) };
		} finally {
			this.#inflight = false;
		}
		this.#changed();
		if (this.#again) {
			this.#again = false;
			void this.#read();
		}
	}

	#changed(): void {
		for (const l of [...this.#listeners]) l();
	}
}

// --- the tree -------------------------------------------------------------------------

/** The client a session or chat belongs to; the Sessions folders tag it (ADR-0134 Q2). */
export type ClientId = 'claude-code' | 'cursor' | 'other';
export const CLIENT_NAMES: Record<ClientId, string> = { 'claude-code': 'Claude Code', cursor: 'Cursor', other: 'Other' };

/**
 * The client a live session's name says: `claude-code` is Claude Code, a name beginning
 * `cursor` (cursor-agent says `Cursor`, the IDE `cursor-vscode`) is Cursor, anything else,
 * a paired peer's link included, is Other.
 */
export function clientOf(name: string | null | undefined): ClientId {
	if (name === 'claude-code') return 'claude-code';
	if (name !== null && name !== undefined && name.toLowerCase().startsWith('cursor')) return 'cursor';
	return 'other';
}

export interface ChatRow {
	/** Stable across reads: the store and the id. */
	key: string;
	/** The title the client shows; "Untitled · <date>" when it has none. */
	title: string;
	/** The linked worktree's name; null for the main tree. */
	worktree: string | null;
	/** The chat's last change. */
	updatedAt: string;
	/** The client's store: the row's tag (ADR-0134). */
	tag: Chat['store'];
	/**
	 * Whether the chat is connected to Droidyard (ADR-0134 Q2): `connected` when a live session
	 * names it; otherwise `unknown` where an unclaimed live session of its client sits in its
	 * folder (a Cursor connection carries no chat id, so the join is not guessed), else
	 * `not-connected`.
	 */
	presence: 'connected' | 'not-connected' | 'unknown';
	/** The live session connected to this chat. */
	live?: AgentRow;
	/** The command that resumes the chat in its client; absent where there is none. */
	resume?: string;
	/** The workspace a click on a row that is not live selects: the worktree's, else the project's, whichever is open; null when none is (a click opens nothing). */
	workspace: string | null;
}

/** A live session no chat claims: the folder lists it as a connection, not as a chat. */
export interface Connection {
	client: ClientId;
	row: AgentRow;
}

export interface Folder {
	/** The project's main tree, absolute; null is "No project". */
	root: string | null;
	name: string;
	/** The tab's project: first, and its connections are nested on its worktrees (`treeRows`), so `connections` is empty. */
	current: boolean;
	rows: ChatRow[];
	connections: Connection[];
}

const baseName = (path: string): string => rootKey(path).split(/[\\/]/).pop() || path;

/** `cd '<dir>' && claude --resume <id>`: a `'` in the directory is written `'\''`. The title never enters it. */
export function resumeCommand(chat: Pick<Chat, 'store' | 'id' | 'project' | 'worktree'>): string | undefined {
	const dir = `'${(chat.worktree ?? chat.project).replace(/'/g, `'\\''`)}'`;
	switch (chat.store) {
		case 'claude-code':
			return `cd ${dir} && claude --resume ${chat.id}`;
		case 'cursor-agent':
			return `cd ${dir} && cursor-agent --resume ${chat.id}`;
		default:
			return undefined; // the Cursor IDE has no resume command
	}
}

function titleOf(chat: Chat): string {
	const t = chat.title?.trim();
	return t ? t : `Untitled · ${chat.updatedAt.slice(0, 10)}`;
}

/** Session id → the title of the chat that names it, for the worktree rows' nested sessions (`treeRows`). */
export function titlesBySession(chats: readonly Chat[]): Map<string, string> {
	const out = new Map<string, string>();
	for (const c of chats) if (c.session !== undefined) out.set(c.session, titleOf(c));
	return out;
}

/**
 * The Sessions folders (ADR-0134 Q1, Q2): one per project, the tab's project (`tabMain`)
 * first, then by newest chat (a connection counts as newest: it is live), "No project" last.
 * Chats are rows, newest first; a live session no chat claims is no row but a connection of its
 * project's folder (not the tab's: those nest on its worktrees); a project with a connection
 * and no chat still has a folder. A session with no listed workspace (a paired peer's link,
 * an MCP session not yet bound) is a connection of "No project".
 */
export function chatFolders(chats: readonly Chat[], sessions: readonly AgentRow[], projects: readonly ProjectRow[], tabMain: string | null): Folder[] {
	const byId = new Map(projects.map((p) => [p.id, p]));
	const byRoot = new Map(projects.map((p) => [rootKey(p.root), p]));
	const rowsBySession = new Map(sessions.map((s) => [s.key, s]));
	const tabKey = tabMain === null ? null : rootKey(tabMain);
	const claimed = new Set<string>();

	interface Entry extends ChatRow {
		time: number;
		client: ClientId;
	}
	const folders = new Map<string, { folder: Folder; entries: Entry[]; unclaimed: Connection[] }>();
	const folderFor = (root: string | null) => {
		const key = root === null ? '' : rootKey(root);
		let f = folders.get(key);
		if (!f) {
			const current = root !== null && key === tabKey;
			folders.set(key, (f = { folder: { root, name: root === null ? 'No project' : baseName(root), current, rows: [], connections: [] }, entries: [], unclaimed: [] }));
		}
		return f;
	};

	for (const c of chats) {
		const live = c.session === undefined ? undefined : rowsBySession.get(c.session);
		if (live) claimed.add(live.key);
		const wt = c.worktree === undefined ? undefined : byRoot.get(rootKey(c.worktree));
		const main = byRoot.get(rootKey(c.project));
		const resume = resumeCommand(c);
		const time = Date.parse(c.updatedAt);
		folderFor(c.project).entries.push({
			key: `${c.store}:${c.id}`,
			title: titleOf(c),
			worktree: c.worktree === undefined ? null : baseName(c.worktree),
			updatedAt: c.updatedAt,
			tag: c.store,
			presence: live ? 'connected' : 'not-connected',
			...(live ? { live } : {}),
			...(resume !== undefined ? { resume } : {}),
			workspace: wt?.open ? wt.id : main?.open ? main.id : null,
			time: Number.isNaN(time) ? 0 : time,
			client: c.client === 'claude' ? 'claude-code' : 'cursor',
		});
	}
	for (const s of sessions) {
		if (claimed.has(s.key)) continue;
		const ws = s.workspace === null ? undefined : byId.get(s.workspace);
		const f = folderFor(ws === undefined ? null : (ws.mainRoot ?? ws.root));
		f.unclaimed.push({ client: clientOf(s.name), row: s });
	}

	const out: { folder: Folder; newest: number }[] = [];
	for (const f of folders.values()) {
		// A chat the join did not reach might still be that unclaimed connection (ADR-0134 Q2).
		for (const e of f.entries) {
			if (e.presence === 'not-connected' && f.unclaimed.some((u) => u.client === e.client)) e.presence = 'unknown';
		}
		f.entries.sort((a, b) => b.time - a.time);
		f.folder.rows = f.entries.map(({ time: _time, client: _client, ...row }) => row);
		if (!f.folder.current) f.folder.connections = f.unclaimed;
		if (f.folder.rows.length === 0 && f.folder.connections.length === 0) continue;
		out.push({ folder: f.folder, newest: f.folder.connections.length > 0 ? Number.MAX_SAFE_INTEGER : (f.entries[0]?.time ?? 0) });
	}
	const rank = (f: Folder) => (f.current ? 0 : f.root === null ? 2 : 1);
	out.sort((a, b) => rank(a.folder) - rank(b.folder) || b.newest - a.newest);
	return out.map((o) => o.folder);
}

/** Chats shown per project before "N more" (ADR-0127 §Decision 4). */
export const PER_PROJECT = 8;

/** The first PER_PROJECT rows, or all of them once opened; `more` is how many the cut hides. */
export function cut<T>(rows: readonly T[], opened: boolean): { shown: readonly T[]; more: number } {
	if (opened || rows.length <= PER_PROJECT) return { shown: rows, more: 0 };
	return { shown: rows.slice(0, PER_PROJECT), more: rows.length - PER_PROJECT };
}

const STORE_NAMES: Record<ChatSource['store'], string> = { 'claude-code': 'Claude Code', 'cursor-agent': 'cursor-agent', 'cursor-ide': 'Cursor IDE' };

/** One note per store that was not read: failed (with the cause the operating system gave) or absent. */
export function sourceNotes(sources: readonly ChatSource[]): string[] {
	const out: string[] = [];
	for (const s of sources) {
		if (s.state === 'failed') out.push(`${STORE_NAMES[s.store]} chats could not be read${s.cause ? `: ${s.cause}` : ''}`);
		else if (s.state === 'absent') out.push(`${STORE_NAMES[s.store]}: no chat store on this machine`);
	}
	return out;
}

/** "now", "5 m", "3 h", "2 d": how long ago a chat changed. */
export function agoText(ms: number): string {
	const m = Math.floor(Math.max(0, ms) / 60_000);
	if (m < 1) return 'now';
	if (m < 60) return `${m} m`;
	const h = Math.floor(m / 60);
	return h < 24 ? `${h} h` : `${Math.floor(h / 24)} d`;
}
