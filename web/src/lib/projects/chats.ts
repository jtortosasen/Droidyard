// The chats of the user's assistant clients and the Sessions tree built from them (ADR-0127
// §Decision 4). `session.chats` is a stateless read of the clients' own local stores, so the
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

/** The client groups of the Sessions tree, in the order they are shown. */
export type ClientId = 'claude-code' | 'cursor' | 'other';
export const CLIENT_NAMES: Record<ClientId, string> = { 'claude-code': 'Claude Code', cursor: 'Cursor', other: 'Other' };
const CLIENT_ORDER: ClientId[] = ['claude-code', 'cursor', 'other'];

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
	/** Stable across reads: the store and the id, or the session id of a live session no chat claims. */
	key: string;
	/** The title the client shows; "Untitled · <date>" when it has none; a live-only row reads as its session. */
	title: string;
	/** The linked worktree's name; null for the main tree. */
	worktree: string | null;
	/** The chat's last change; null for a live session no chat claims. */
	updatedAt: string | null;
	/** The live session connected to this chat, or the unclaimed live session itself. */
	live?: AgentRow;
	/** The command that resumes the chat in its client; absent where there is none. */
	resume?: string;
	/** The workspace a click on a row that is not live selects: the worktree's, else the project's, whichever is open; null when none is (a click opens nothing). */
	workspace: string | null;
}

export interface ProjectNode {
	/** The project's main tree, absolute; null is "No project". */
	root: string | null;
	name: string;
	rows: ChatRow[];
}

export interface ClientNode {
	id: ClientId;
	name: string;
	projects: ProjectNode[];
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

/**
 * Clients → projects → chats. A chat that names a live session takes that session's state and
 * makes no second row; a live session no chat claims is a row of its own under the client its
 * name says and its workspace's project. Projects are ordered by their newest chat (a live row
 * counts as newest), chats newest first.
 */
export function chatTree(chats: readonly Chat[], sessions: readonly AgentRow[], projects: readonly ProjectRow[]): ClientNode[] {
	const byId = new Map(projects.map((p) => [p.id, p]));
	const byRoot = new Map(projects.map((p) => [rootKey(p.root), p]));
	const rowsBySession = new Map(sessions.map((s) => [s.key, s]));
	const claimed = new Set<string>();

	interface Entry extends ChatRow {
		time: number;
	}
	const groups = new Map<ClientId, Map<string, { node: ProjectNode; entries: Entry[] }>>();
	const place = (client: ClientId, root: string | null, entry: Entry) => {
		let g = groups.get(client);
		if (!g) groups.set(client, (g = new Map()));
		const key = root === null ? '' : rootKey(root);
		let p = g.get(key);
		if (!p) g.set(key, (p = { node: { root, name: root === null ? 'No project' : baseName(root), rows: [] }, entries: [] }));
		p.entries.push(entry);
	};

	for (const c of chats) {
		const live = c.session === undefined ? undefined : rowsBySession.get(c.session);
		if (live) claimed.add(live.key);
		const wt = c.worktree === undefined ? undefined : byRoot.get(rootKey(c.worktree));
		const main = byRoot.get(rootKey(c.project));
		const resume = resumeCommand(c);
		const time = Date.parse(c.updatedAt);
		place(c.client === 'claude' ? 'claude-code' : 'cursor', c.project, {
			key: `${c.store}:${c.id}`,
			title: titleOf(c),
			worktree: c.worktree === undefined ? null : baseName(c.worktree),
			updatedAt: c.updatedAt,
			...(live ? { live } : {}),
			...(resume !== undefined ? { resume } : {}),
			workspace: wt?.open ? wt.id : main?.open ? main.id : null,
			time: Number.isNaN(time) ? 0 : time,
		});
	}
	for (const s of sessions) {
		if (claimed.has(s.key)) continue;
		const ws = s.workspace === null ? undefined : byId.get(s.workspace);
		const root = ws === undefined ? null : (ws.mainRoot ?? ws.root);
		place(clientOf(s.name), root, {
			key: s.key,
			title: s.label,
			worktree: ws?.mainRoot === undefined ? null : ws.name,
			updatedAt: null,
			live: s,
			workspace: ws?.id ?? null,
			time: Number.MAX_SAFE_INTEGER,
		});
	}

	const out: ClientNode[] = [];
	for (const id of CLIENT_ORDER) {
		const g = groups.get(id);
		if (!g) continue;
		const nodes = [...g.values()].map((p) => {
			p.entries.sort((a, b) => b.time - a.time);
			p.node.rows = p.entries.map(({ time: _time, ...row }) => row);
			return { node: p.node, newest: p.entries[0]!.time };
		});
		nodes.sort((a, b) => b.newest - a.newest);
		out.push({ id, name: CLIENT_NAMES[id], projects: nodes.map((n) => n.node) });
	}
	return out;
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
