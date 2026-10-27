// The tab's project as its worktrees (ADR-0134, ADR-0089 §1): the main tree tagged primary,
// then the linked ones, each with the live sessions bound there. `workspace.describe` lists
// the project's linked worktrees whether or not they are open; `workspace.watch` lists the
// ones the daemon knows; the rows are the two merged by root.
//
// `TreesModel` reads the tab's workspace by root: when it is shown, one second after
// `changed()` (a burst is one read), every 30 seconds while shown, and once when a lost
// connection opens again. An answer for a root no longer shown is dropped; a failed read keeps
// the last list and a note.
//
// Every branch name, title and session label is untrusted text (ADR-0015 §1): data here, a
// text node in the panel.
import type { Params, Result } from '../api/capabilities.gen';
import { ConnectionError, type CallOptions, type ConnectionState } from '../api/client';
import { failure, type Failure } from '../build/failure';
import { clientOf, POLL_MS, REFRESH_DELAY_MS, type ClientId } from './chats';
import { rootKey, type ProjectRow } from './projects';
import { holderOf, type AgentRow } from './sessions';

export type DescribedWorktree = NonNullable<Result<'workspace.describe'>['worktrees']>[number];

export interface TreesClient {
	call(name: 'workspace.describe', params: Params<'workspace.describe'>, opts?: CallOptions): Promise<Result<'workspace.describe'>>;
}

export interface TreesView {
	/** The linked worktrees the last read gave; null until one answered (or after the root changed). */
	described: readonly DescribedWorktree[] | null;
	/** The last read failed: `described` is the list before it. */
	error: Failure | null;
}

export class TreesModel {
	#client: TreesClient;
	#root: string | null = null;
	#view: TreesView = { described: null, error: null };
	#timer: ReturnType<typeof setInterval> | null = null;
	#soon: ReturnType<typeof setTimeout> | null = null;
	#seq = 0;
	#waiting = false;
	#listeners = new Set<() => void>();

	constructor(client: TreesClient) {
		this.#client = client;
	}

	get view(): TreesView {
		return this.#view;
	}

	subscribe(listener: () => void): () => void {
		this.#listeners.add(listener);
		return () => {
			this.#listeners.delete(listener);
		};
	}

	/** The workspace the tab shows, by root; null shows none. The same root again changes nothing. */
	show(root: string | null): void {
		if (root === this.#root) return;
		this.#root = root;
		this.#seq++; // an answer still in flight is for the root before
		this.#waiting = false;
		if (this.#timer !== null) clearInterval(this.#timer);
		if (this.#soon !== null) clearTimeout(this.#soon);
		this.#timer = null;
		this.#soon = null;
		this.#view = { described: null, error: null };
		this.#changed();
		if (root === null) return;
		void this.#read();
		this.#timer = setInterval(() => void this.#read(), POLL_MS);
	}

	/** A `session.state` event or a change of the project list: read again in a second; a burst shares the read. */
	changed(): void {
		if (this.#root === null || this.#soon !== null) return;
		this.#soon = setTimeout(() => {
			this.#soon = null;
			void this.#read();
		}, REFRESH_DELAY_MS);
	}

	/** A lost connection reads again once it is open. */
	onConnection(state: ConnectionState): void {
		if (state === 'open' && this.#waiting && this.#root !== null) {
			this.#waiting = false;
			void this.#read();
		}
	}

	async #read(): Promise<void> {
		const root = this.#root;
		if (root === null) return;
		const seq = ++this.#seq;
		try {
			const r = await this.#client.call('workspace.describe', {}, { workspace: root });
			if (seq !== this.#seq) return;
			this.#view = { described: r.worktrees ?? [], error: null };
			this.#waiting = false;
		} catch (e) {
			if (seq !== this.#seq) return;
			if (e instanceof ConnectionError && e.kind === 'lost') this.#waiting = true;
			else this.#view = { ...this.#view, error: failure(e) };
		}
		this.#changed();
	}

	#changed(): void {
		for (const l of [...this.#listeners]) l();
	}
}

// --- the rows -------------------------------------------------------------------------

export interface TreeSession {
	row: AgentRow;
	client: ClientId;
	/** The joined chat's title, else `<label> connected` (two unclaimed `Cursor` sessions stay two, ADR-0048 §Alternatives considered). */
	title: string;
}

export interface TreeRow {
	root: string;
	name: string;
	branch?: string;
	/** The project's main tree. */
	primary: boolean;
	/** The workspace id when the daemon lists it. */
	workspace?: string;
	open: boolean;
	state: string;
	trusted: boolean;
	/** Its workspace is the tab's. */
	current: boolean;
	/** Listed, closed, and absent from a loaded describe: the worktree was removed or moved. */
	gone: boolean;
	sessions: TreeSession[];
	/** Who holds it (ADR-0094): a session of this panel, else the other chat `describe` names. */
	holder?: { label: string; idle: boolean };
}

const baseName = (root: string): string => rootKey(root).split(/[\\/]/).pop() || root;

/**
 * The tab's project as rows: its main tree (`primary`) first, then the linked worktrees of
 * `described` merged by `rootKey` with the listed rows that name the project as their
 * `mainRoot`, in root order. `described` null (not read yet, failed, or no git) gives the
 * listed rows only. `tab` null gives none.
 */
export function treeRows(
	tab: ProjectRow | null,
	rows: readonly ProjectRow[],
	described: readonly DescribedWorktree[] | null,
	sessions: readonly AgentRow[],
	titles: ReadonlyMap<string, string>,
): TreeRow[] {
	if (tab === null) return [];
	const main = rootKey(tab.mainRoot ?? tab.root);
	const mainListed = rows.find((r) => r.mainRoot === undefined && rootKey(r.root) === main);

	const nested = (workspace: string | undefined): TreeSession[] =>
		workspace === undefined
			? []
			: sessions
					.filter((s) => s.workspace === workspace)
					.map((row) => ({ row, client: clientOf(row.name), title: titles.get(row.key) ?? `${row.label} connected` }));
	const holderFor = (workspace: string | undefined, by: DescribedWorktree['occupiedBy']): TreeRow['holder'] | undefined => {
		const h = workspace === undefined ? undefined : holderOf(sessions, workspace);
		if (h) return { label: h.label, idle: h.idle };
		return by === undefined ? undefined : { label: by.name ?? 'another chat', idle: by.stale === true };
	};
	const build = (root: string, listed: ProjectRow | undefined, primary: boolean, d?: DescribedWorktree): TreeRow => {
		const holder = holderFor(listed?.id, d?.occupiedBy);
		return {
			root,
			name: listed?.name ?? baseName(root),
			...(d?.branch !== undefined ? { branch: d.branch } : {}),
			primary,
			...(listed ? { workspace: listed.id } : {}),
			open: listed?.open ?? false,
			state: listed?.state ?? 'closed',
			trusted: listed?.trusted ?? false,
			current: listed !== undefined && listed.id === tab.id,
			gone: !primary && listed !== undefined && !listed.open && described !== null && d === undefined,
			sessions: nested(listed?.id),
			...(holder ? { holder } : {}),
		};
	};

	const out = [build(mainListed?.root ?? tab.mainRoot ?? tab.root, mainListed, true)];
	const linked = new Map<string, { root: string; listed?: ProjectRow; d?: DescribedWorktree }>();
	for (const d of described ?? []) {
		const k = rootKey(d.root);
		if (k !== main) linked.set(k, { root: d.root, d });
	}
	for (const r of rows) {
		if (r.mainRoot === undefined || rootKey(r.mainRoot) !== main) continue;
		const k = rootKey(r.root);
		linked.set(k, { ...linked.get(k), root: r.root, listed: r });
	}
	for (const [, l] of [...linked].sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0))) out.push(build(l.root, l.listed, false, l.d));
	return out;
}
