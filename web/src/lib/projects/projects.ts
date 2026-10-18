// The projects model (ADR-0047, ADR-0045 §3, §4): the projects this installation has known,
// live. `workspace.watch` answers with the index merged with the live states, and its
// `workspace.state` events keep the rows current, so a workspace an agent opens through MCP
// shows without a reload. Selecting a row selects its project, opening it first when it is
// closed; "Open folder..." raises the daemon's folder dialog (`workspace.chooseRoot`) and
// opens the root it answers with; where the dialog is unavailable the user types a root, as
// 2b did. Opening never trusts.
//
// One tab in all (ADR-0084): a `show` event, which `droidyard` causes through `workspace.show`,
// selects its project as a click does. A closed project can be removed from the list
// (`workspace.forget`, ADR-0086); a `forgotten` event, from any tab, drops the row.
//
// The watch lives with the page (start/stop); a lost connection or `events.gap` starts it
// again.
import { eventTopics, type EventPayload, type Params, type Result } from '../api/capabilities.gen';
import {
	ConnectionError,
	type CallOptions,
	type ConnectionState,
	type EventFrame,
	type SubscribeHandlers,
	type Subscription,
} from '../api/client';
import { failure, failureText, type Failure } from '../build/failure';
import type { Route } from '../start/route';

/** The daemon's list is at most 50 (workspace.watch); events never grow it past that. */
export const MAX_ROWS = 50;

const TOPIC_STATE = eventTopics['workspace.watch'];
const TOPIC_GAP = 'events.gap';

type Calls = 'workspace.open' | 'workspace.chooseRoot' | 'workspace.forget' | 'system.capabilities' | 'events.unsubscribe';
type StateEvent = EventPayload<'workspace.watch', 'state'>;

export interface ProjectsClient {
	call<N extends Calls>(name: N, params: Params<N>, opts?: CallOptions): Promise<Result<N>>;
	subscribe(
		name: 'workspace.watch',
		params: Params<'workspace.watch'>,
		handlers: SubscribeHandlers,
	): Promise<Subscription & { readonly result: Result<'workspace.watch'> }>;
}

export interface ProjectRow {
	id: string;
	name: string;
	root: string;
	lastOpenedAt: string;
	state: string;
	trusted: boolean;
	/** Open in the daemon now: anything but closed. */
	open: boolean;
	/** The project's main tree, when `root` is a linked git worktree of it (ADR-0081). */
	mainRoot?: string;
}

export type Chooser = 'unknown' | 'available' | 'unavailable';
export type WatchState = 'idle' | 'starting' | 'watching' | 'waiting' | 'error' | 'ended';

export interface ProjectsView {
	rows: readonly ProjectRow[];
	truncated: boolean;
	watch: { state: WatchState; detail: string };
	/** Whether the folder dialog can be raised; anything but available shows the typed field. */
	chooser: Chooser;
	chooserReason: string;
	/** A dialog is on the user's screen. */
	choosing: boolean;
	/** An open call is in flight. */
	opening: boolean;
	error: Failure | null;
}

export class ProjectsModel {
	#client: ProjectsClient;
	#navigate: (workspaceId: string) => void;
	#onShow: (() => void) | undefined;
	#rows = new Map<string, ProjectRow>();
	#truncated = false;
	#watch: { state: WatchState; detail: string } = { state: 'idle', detail: '' };
	#chooser: Chooser = 'unknown';
	#chooserReason = '';
	#choosing = false;
	#opening = false;
	#error: Failure | null = null;
	#run: object | null = null;
	#sub: Subscription | null = null;
	#waiting = false;
	#listeners = new Set<() => void>();
	#view: ProjectsView | null = null;

	/** `onShow` runs before a `show` event selects its project: a selection the daemon made stays, as a project row's does. */
	constructor(client: ProjectsClient, navigate: (workspaceId: string) => void, onShow?: () => void) {
		this.#client = client;
		this.#navigate = navigate;
		this.#onShow = onShow;
	}

	get view(): ProjectsView {
		this.#view ??= {
			rows: [...this.#rows.values()].sort((a, b) => time(b.lastOpenedAt) - time(a.lastOpenedAt)),
			truncated: this.#truncated,
			watch: this.#watch,
			chooser: this.#chooser,
			chooserReason: this.#chooserReason,
			choosing: this.#choosing,
			opening: this.#opening,
			error: this.#error,
		};
		return this.#view;
	}

	subscribe(listener: () => void): () => void {
		this.#listeners.add(listener);
		return () => {
			this.#listeners.delete(listener);
		};
	}

	/** Read what the dialog can do, and follow the workspaces. */
	start(): void {
		void this.#readChooser();
		this.#begin();
	}

	/** The page is left: the watch ends; the rows stay until the next start replaces them. */
	stop(): void {
		this.#run = null;
		this.#waiting = false;
		this.#release();
		this.#setWatch('idle', '');
	}

	onConnection(state: ConnectionState): void {
		if (state === 'open' && this.#waiting) {
			this.#waiting = false;
			this.#begin();
			void this.#readChooser();
		} else if ((state === 'unauthorized' || state === 'closed') && this.#run !== null) {
			this.#waiting = false;
			this.#setWatch('ended', state === 'unauthorized' ? 'the daemon refused the token' : 'the connection was closed');
		}
	}

	/** Why the project the address named could not be opened: shown in the panel. */
	showError(f: Failure): void {
		this.#error = f;
		this.#changed();
	}

	/** A click on a project: an open one is shown at once, a closed one is opened by its id first. */
	select(id: string): Promise<void> {
		if (this.#rows.get(id)?.open === true) {
			this.#error = null;
			this.#changed();
			this.#navigate(id);
			return Promise.resolve();
		}
		return this.#open(id);
	}

	/** Remove a closed project from the list; an open one is not offered, and the daemon refuses it. */
	async forget(id: string): Promise<void> {
		if (this.#rows.get(id)?.open !== false) return;
		this.#error = null;
		try {
			await this.#client.call('workspace.forget', { workspace: id });
			// Only if still closed: an open that raced the forget brought the row back.
			if (this.#rows.get(id)?.open === false) this.#rows.delete(id);
		} catch (e) {
			this.#error = failure(e);
		}
		this.#changed();
	}

	/** The typed root, where the folder dialog is unavailable. */
	openRoot(root: string): Promise<void> {
		return this.#open(root.trim());
	}

	/** "Open folder...": the daemon's dialog; a dismissed dialog opens nothing. */
	async chooseFolder(): Promise<void> {
		if (this.#choosing || this.#opening) return;
		this.#choosing = true;
		this.#error = null;
		this.#changed();
		let root: string | undefined;
		try {
			const r = await this.#client.call('workspace.chooseRoot', {});
			if (!r.dismissed) root = r.root;
		} catch (e) {
			this.#error = failure(e);
		} finally {
			this.#choosing = false;
			this.#changed();
		}
		if (root !== undefined && root !== '') await this.#open(root);
	}

	async #open(target: string): Promise<void> {
		if (target === '' || this.#opening) return;
		this.#opening = true;
		this.#error = null;
		this.#changed();
		let id: string | undefined;
		try {
			id = (await this.#client.call('workspace.open', {}, { workspace: target })).id;
		} catch (e) {
			this.#error = failure(e);
		} finally {
			this.#opening = false;
			this.#changed();
		}
		if (id !== undefined) this.#navigate(id);
	}

	async #readChooser(): Promise<void> {
		try {
			const r = await this.#client.call('system.capabilities', {});
			const c = r.capabilities?.find((x) => x.name === 'workspace.chooseRoot');
			this.#chooser = c?.available === true ? 'available' : 'unavailable';
			this.#chooserReason = c?.available === true ? '' : (c?.unavailableReason ?? '');
			this.#changed();
		} catch {
			// Unknown stays: the typed field is offered, it always works.
		}
	}

	// --- the watch -------------------------------------------------------------------

	#begin(): void {
		const run = {};
		this.#run = run;
		this.#waiting = false;
		this.#release();
		this.#setWatch('starting', '');
		const handlers: SubscribeHandlers = {
			onEvent: (frame) => {
				if (this.#run === run) this.#event(frame);
			},
			onEnd: (why) => {
				if (this.#run !== run) return;
				this.#sub = null;
				if (why.kind === 'lost') {
					this.#waiting = true;
					this.#setWatch('waiting', 'the connection was lost');
				} else {
					this.#setWatch('ended', why.message);
				}
			},
		};
		this.#client.subscribe('workspace.watch', {}, handlers).then(
			(sub) => {
				if (this.#run !== run) {
					this.#unsubscribe(sub);
					return;
				}
				this.#sub = sub;
				// The list was taken after the subscription opened, and the session writes the
				// first event only after this answer: the snapshot replaces the rows and the
				// events merge into it by id.
				this.#rows = new Map(sub.result.workspaces.map((w) => [w.id, { ...w, open: w.state !== 'closed' }]));
				this.#truncated = sub.result.truncated;
				this.#setWatch('watching', '');
			},
			(err: unknown) => {
				if (this.#run !== run) return;
				if (err instanceof ConnectionError && err.kind === 'lost') {
					this.#waiting = true;
					this.#setWatch('waiting', 'the connection was lost');
				} else {
					this.#setWatch(err instanceof ConnectionError ? 'ended' : 'error', failureText(failure(err)));
				}
			},
		);
	}

	#event(frame: EventFrame): void {
		const ev = frame.event;
		if (ev.topic === TOPIC_GAP) {
			// Events were lost: the list is read again (workspace.watch's contract).
			this.#begin();
			return;
		}
		if (ev.topic !== TOPIC_STATE || ev.workspace === null) return;
		if (ev.type === 'show') {
			this.#onShow?.();
			void this.select(ev.workspace);
			return;
		}
		if (ev.type === 'forgotten') {
			if (this.#rows.delete(ev.workspace)) this.#changed();
			return;
		}
		if (ev.type !== 'state') return;
		const p = ev.payload as StateEvent;
		const prev = this.#rows.get(ev.workspace);
		this.#rows.set(ev.workspace, {
			id: ev.workspace,
			name: p.name,
			root: p.root,
			// An open refreshes the index's lastOpenedAt; a row the snapshot did not hold is new.
			lastOpenedAt: prev === undefined || p.state === 'opening' ? ev.time : prev.lastOpenedAt,
			state: p.state,
			trusted: p.trusted,
			open: p.state !== 'closed',
			...(p.mainRoot !== undefined ? { mainRoot: p.mainRoot } : {}),
		});
		if (this.#rows.size > MAX_ROWS) {
			const oldest = [...this.#rows.values()].sort((a, b) => time(a.lastOpenedAt) - time(b.lastOpenedAt))[0];
			if (oldest) this.#rows.delete(oldest.id);
		}
		this.#changed();
	}

	#setWatch(state: WatchState, detail: string): void {
		this.#watch = { state, detail };
		this.#changed();
	}

	#release(): void {
		const sub = this.#sub;
		this.#sub = null;
		if (sub) this.#unsubscribe(sub);
	}

	#unsubscribe(sub: Subscription): void {
		sub.stop();
		this.#client.call('events.unsubscribe', { subscriptionId: sub.subscriptionId }).catch(() => {
			// The session may be gone; its subscriptions went with it.
		});
	}

	#changed(): void {
		this.#view = null;
		for (const l of [...this.#listeners]) l();
	}
}

const time = (iso: string): number => {
	const t = Date.parse(iso);
	return Number.isNaN(t) ? 0 : t;
};

/** What the page shows (ADR-0047 §2). */
export type Landing = { kind: 'waiting' } | { kind: 'project'; workspace: string } | { kind: 'panel' };

/**
 * The address names a project, and that wins. Without `w`: the newest open workspace once
 * the list is read; none open, or the list unreadable, or a `w` the daemon refused
 * (`refused`: the reason is in the panel), the pinned panel.
 */
export function landing(route: Route, view: ProjectsView, refused: boolean): Landing {
	if (route.view === 'project') return { kind: 'project', workspace: route.workspace };
	if (refused) return { kind: 'panel' };
	if (view.watch.state === 'idle' || view.watch.state === 'starting') return { kind: 'waiting' };
	const newest = view.rows.find((r) => r.open);
	return newest ? { kind: 'project', workspace: newest.id } : { kind: 'panel' };
}

// --- the panel by project (ADR-0083 §2) ----------------------------------------------

/** A root without its trailing separators, so "/a/b/" and "/a/b" are one tree. */
export const rootKey = (root: string): string => root.replace(/[\\/]+$/, '');
const baseName = (root: string): string => rootKey(root).split(/[\\/]/).pop() || root;

/** The part of a session row the grouping reads. */
export interface GroupedSession {
	key: string;
	you: boolean;
	workspace: string | null;
}

export interface ProjectGroup<S extends GroupedSession> {
	/** The main tree's root: the group's identity. */
	key: string;
	/** The main tree's row; null when it is not listed (the group row then stands for `root`). */
	project: ProjectRow | null;
	name: string;
	root: string;
	/** Sessions in this project, in the main tree or in a worktree, each naming where. */
	sessions: { session: S; where: string; workspace: string }[];
	/** The worktrees with no session in them. */
	worktrees: ProjectRow[];
}

export interface Grouping<S extends GroupedSession> {
	groups: ProjectGroup<S>[];
	/** Sessions with no workspace, or one the list does not hold. */
	loose: S[];
}

/**
 * Split the rows by project (ADR-0083 §2): a row with `mainRoot` nests under the row whose
 * root that is, else under a group row for that root; no worktree is ever a top-level row.
 * Groups keep the order of their first row (rows arrive newest first). A session sits under
 * its workspace's project and names the worktree's name, or "main".
 */
export function groupProjects<S extends GroupedSession>(rows: readonly ProjectRow[], sessions: readonly S[]): Grouping<S> {
	const groups = new Map<string, ProjectGroup<S>>();
	const mainOf = (r: ProjectRow): string => rootKey(r.mainRoot ?? r.root);
	for (const r of rows) {
		const key = mainOf(r);
		let g = groups.get(key);
		if (!g) groups.set(key, (g = { key, project: null, name: baseName(key), root: key, sessions: [], worktrees: [] }));
		if (r.mainRoot === undefined) {
			g.project = r;
			g.name = r.name;
			g.root = r.root;
		}
	}
	const byId = new Map(rows.map((r) => [r.id, r]));
	const loose: S[] = [];
	const inside = new Set<string>();
	for (const s of sessions) {
		const r = s.workspace === null ? undefined : byId.get(s.workspace);
		if (r === undefined) {
			loose.push(s);
			continue;
		}
		groups.get(mainOf(r))!.sessions.push({ session: s, where: r.mainRoot === undefined ? 'main' : r.name, workspace: r.id });
		inside.add(r.id);
	}
	for (const r of rows) if (r.mainRoot !== undefined && !inside.has(r.id)) groups.get(mainOf(r))!.worktrees.push(r);
	return { groups: [...groups.values()], loose };
}

/** The labels of the sessions whose workspace root is each path, for the Git panel's worktree rows (ADR-0083 §4). */
export function sessionsByRoot(rows: readonly ProjectRow[], sessions: readonly (GroupedSession & { label: string })[]): Map<string, string[]> {
	const byId = new Map(rows.map((r) => [r.id, r]));
	const out = new Map<string, string[]>();
	for (const s of sessions) {
		if (s.you || s.workspace === null) continue;
		const r = byId.get(s.workspace);
		if (r === undefined) continue;
		const key = rootKey(r.root);
		out.set(key, [...(out.get(key) ?? []), s.label]);
	}
	return out;
}
