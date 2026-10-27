// The session rows of the projects panel (ADR-0127 §Decision 4, ADR-0083, ADR-0048). A row is
// a session `session.watch` lists now (an idle session is a row, and several may sit in one
// worktree); it leaves when the watch reports it ended. A session known only by its
// operations is not a row, and the user's own `ui` and `cli` commands are not one (the
// "You" row went with ADR-0127; `keyOf` still gives them the key YOU, which Activity filters
// by).
//
//   - Two labels (ADR-0048 §Alternatives: two agents are never merged). A row's `label` is
//     its session name, numbered " 2", " 3" among the rows listed now, so the panel reads
//     `claude-code` while one such session lives. `actorLabel(id)` is kept for every session
//     the tab has seen, numbered in first-seen order and never renumbered, so Activity,
//     the build status and "in use by" never give an ended session's operations and a live
//     one's the same name. No name: "agent".
//   - A listed session's workspace is the one the watch says; none is the "No project" group.
//   - A row runs while any of its operations is not terminal. Unread is a terminal event
//     since the row was last selected, per tab, in memory only.
//   - The UI follows a session and never moves it (ADR-0083 §3): `follow` names the session
//     whose moves the selection takes; `unfollow` is a selection made from a project row.
//     Both notify the listeners, as the panel marks the followed session (ADR-0134).
//     `routed(workspace)` is the page's route changing: it ends the follow unless the route is
//     the followed session's current workspace, so the follow's own navigation keeps it and
//     any other navigation (a tab, a link, the address) ends it (ADR-0134).
//   - A session may occupy a linked worktree and devices (ADR-0094): its row says which and
//     whether it is idle, and the owner's Release (`release`) ends one occupation without
//     moving the session.
import { capabilityInfo, eventTopics, type CapabilityName, type EventPayload, type Params, type Result } from '../api/capabilities.gen';
import { CapabilityError, ConnectionError, type ConnectionState, type EventFrame, type SubscribeHandlers, type Subscription } from '../api/client';
import { failure, failureText, type Failure } from '../build/failure';
import { isTerminal, type Operation, type OperationState } from '../ops/activity';

/** The key of the user's own commands in Activity; there is no row for it. */
export const YOU = 'you';

export interface SessionsSource {
	readonly operations: readonly Operation[];
	subscribe(listener: () => void): () => void;
}

export interface RunningOperation {
	id: string;
	capability: string;
	startedAt: string;
}

export interface AgentRow {
	/** The session id. */
	key: string;
	/** The panel's label: the name, numbered among the rows listed now. */
	label: string;
	/** The session's own name as it said it; null when it gave none. */
	name: string | null;
	/** The project the row belongs to; null is the "No project" group. */
	workspace: string | null;
	running: boolean;
	/** The state of the newest operation that ended; null before any. */
	result: OperationState | null;
	unread: boolean;
	/** Not terminal, oldest first. */
	operations: RunningOperation[];
	/** The workspace id of the linked worktree the session holds (ADR-0094); null for none. */
	occupies: string | null;
	/** The serials of the devices it holds. */
	devices: readonly string[];
	/** No request for the configured time: another chat may take over what it holds. */
	idle: boolean;
}

export interface SessionsView {
	/** Running rows first; within each, the row with the newest activity first. */
	rows: AgentRow[];
	/** Rows with an operation not terminal: the rail's count. */
	runningAgents: number;
}

interface Track {
	key: string;
	order: number; // first seen: duplicate names are numbered in this order
	name?: string; // the last name the session was seen with
	lastEndAt: number; // the browser's time of the newest terminal event seen; 0 before any
	lastResult: OperationState | null;
	unread: boolean;
}

/** The daemon's side `session.watch` needs (ADR-0083 §1). */
export interface SessionsClient {
	call(name: 'events.unsubscribe', params: Params<'events.unsubscribe'>): Promise<Result<'events.unsubscribe'>>;
	call(name: 'session.release', params: Params<'session.release'>): Promise<Result<'session.release'>>;
	subscribe(
		name: 'session.watch',
		params: Params<'session.watch'>,
		handlers: SubscribeHandlers,
	): Promise<Subscription & { readonly result: Result<'session.watch'> }>;
}

export interface SessionsOptions {
	/** Without it no session is listed but by its operations. */
	client?: SessionsClient;
	/** The session the selection follows moved to this workspace (ADR-0083 §3). */
	onFollow?: (workspace: string) => void;
	/** A `session.state` event arrived: the chats are read again soon (ADR-0127 §Decision 4). */
	onState?: () => void;
}

/** A connected MCP session, as `session.watch` last said. */
interface Listed {
	name?: string;
	workspace?: string;
	occupies?: string;
	devices?: readonly string[];
	stale?: boolean;
}

/** What the watch said of a session, in the model's shape: absent stays absent. */
function listedFrom(s: { name?: string; workspace?: string; occupies?: string; devices?: string[]; stale?: boolean }): Listed {
	return {
		...(s.name !== undefined ? { name: s.name } : {}),
		...(s.workspace !== undefined ? { workspace: s.workspace } : {}),
		...(s.occupies !== undefined ? { occupies: s.occupies } : {}),
		...(s.devices !== undefined ? { devices: s.devices } : {}),
		...(s.stale === true ? { stale: true } : {}),
	};
}

const TOPIC_STATE = eventTopics['session.watch'];
const TOPIC_GAP = 'events.gap';
type StateEvent = EventPayload<'session.watch', 'state'>;

export class SessionsModel {
	#source: SessionsSource;
	#now: () => number;
	#client: SessionsClient | undefined;
	#onFollow: ((workspace: string) => void) | undefined;
	#onState: (() => void) | undefined;
	#tracks = new Map<string, Track>();
	#ended = new Set<string>(); // operations already counted as ended
	#listed = new Map<string, Listed>(); // connected sessions, oldest first
	#gone = new Set<string>(); // sessions the watch reported ended
	#following: string | null = null;
	#selected: string | null = null;
	#off: (() => void) | null = null;
	#run: object | null = null;
	#sub: Subscription | null = null;
	#waiting = false;
	#listeners = new Set<() => void>();
	#order = 0;

	constructor(source: SessionsSource, now: () => number = Date.now, opts: SessionsOptions = {}) {
		this.#source = source;
		this.#now = now;
		this.#client = opts.client;
		this.#onFollow = opts.onFollow;
		this.#onState = opts.onState;
	}

	start(): void {
		if (this.#off) return;
		this.#observe();
		this.#off = this.#source.subscribe(() => {
			this.#observe();
			this.#changed();
		});
		if (this.#client) this.#begin();
	}

	stop(): void {
		this.#off?.();
		this.#off = null;
		this.#run = null;
		this.#waiting = false;
		this.#release();
	}

	/** A lost connection starts the watch again once it is open. */
	onConnection(state: ConnectionState): void {
		if (state === 'open' && this.#waiting) {
			this.#waiting = false;
			this.#begin();
		}
	}

	/** The selection made from this session's row follows its moves; a project row's click ends it. */
	follow(key: string): void {
		this.#following = key;
		this.#changed();
	}

	unfollow(): void {
		if (this.#following === null) return;
		this.#following = null;
		this.#changed();
	}

	/** The route now shows `workspace` (null: none): a route that is not the followed session's own ends the follow (ADR-0134). */
	routed(workspace: string | null): void {
		if (this.#following === null) return;
		if ((this.#listed.get(this.#following)?.workspace ?? null) === workspace) return;
		this.unfollow();
	}

	get following(): string | null {
		return this.#following;
	}

	subscribe(listener: () => void): () => void {
		this.#listeners.add(listener);
		return () => {
			this.#listeners.delete(listener);
		};
	}

	/**
	 * The owner's release of one occupation (ADR-0094 §3): the worktree by its workspace id or
	 * the device by its serial. It never moves a session; the rows change by the watch's events.
	 */
	async release(what: { workspace: string } | { device: string }): Promise<Failure | null> {
		try {
			await this.#client?.call('session.release', what);
			return null;
		} catch (e) {
			// A lost connection is shown by the connection state; a capability error by its code.
			return e instanceof CapabilityError ? failure(e) : null;
		}
	}

	/** The row selected (D30: it filters Activity); null when none. */
	get selectedKey(): string | null {
		return this.#selected;
	}

	/**
	 * The row the user selected: its unread mark clears and stays clear while it is the
	 * selected one (a terminal event on the selected row is read as it happens).
	 */
	select(key: string | null): void {
		this.#selected = key;
		const t = key === null ? undefined : this.#tracks.get(key);
		if (t) t.unread = false;
		this.#changed();
	}

	/** The rows are the sessions the watch lists now; their operations come from the store. */
	get view(): SessionsView {
		const bySession = new Map<string, Operation[]>();
		for (const op of this.#source.operations) {
			const key = forRows(op); // newest first
			if (key === null || key === YOU) continue;
			let list = bySession.get(key);
			if (!list) bySession.set(key, (list = []));
			list.push(op);
		}
		const seen = [...this.#listed.keys()]
			.map((k) => this.#tracks.get(k))
			.filter((t): t is Track => t !== undefined)
			.sort((a, b) => a.order - b.order);
		const labels = labelsFor(
			seen.map((t) => t.key),
			(k) => this.#listed.get(k)?.name,
		);

		const rows: AgentRow[] = [];
		for (const track of seen) {
			const key = track.key;
			const live = (bySession.get(key) ?? []).filter((o) => !isTerminal(o.state));
			const listed = this.#listed.get(key)!;
			rows.push({
				key,
				label: labels.get(key) ?? 'agent',
				name: listed.name ?? null,
				workspace: listed.workspace ?? null,
				running: live.length > 0,
				result: track.lastResult,
				unread: track.unread,
				operations: live
					.map((o) => ({ id: o.id, capability: o.capability, startedAt: o.startedAt }))
					.reverse(),
				occupies: listed.occupies ?? null,
				devices: listed.devices ?? [],
				idle: listed.stale === true,
			});
		}
		const activity = (r: AgentRow) => this.#tracks.get(r.key)?.lastEndAt ?? 0;
		rows.sort((a, b) => Number(b.running) - Number(a.running) || activity(b) - activity(a) || a.label.localeCompare(b.label));
		return { rows, runningAgents: rows.filter((r) => r.running).length };
	}

	/**
	 * The name Activity, the build status and "in use by" give a session: kept for every
	 * session this tab has seen, listed or not, in first-seen order, never renumbered.
	 */
	actorLabels(): ReadonlyMap<string, string> {
		const order = [...this.#tracks.values()].sort((a, b) => a.order - b.order);
		return labelsFor(
			order.map((t) => t.key),
			(k) => this.#tracks.get(k)?.name,
		);
	}

	actorLabel(id: string): string {
		return this.actorLabels().get(id) ?? 'agent';
	}

	/** The session's own name, as the activity records it in `by`; undefined when it gave none. */
	rawName(id: string): string | undefined {
		return this.#tracks.get(id)?.name;
	}

	#track(key: string): Track {
		let t = this.#tracks.get(key);
		if (!t) this.#tracks.set(key, (t = { key, order: this.#order++, lastEndAt: 0, lastResult: null, unread: false }));
		return t;
	}

	// --- the watch (ADR-0083 §1) ------------------------------------------------------

	#begin(): void {
		const client = this.#client;
		if (!client) return;
		const run = {};
		this.#run = run;
		this.#waiting = false;
		this.#release();
		const handlers: SubscribeHandlers = {
			onEvent: (frame) => {
				if (this.#run === run) this.#event(frame);
			},
			onEnd: (why) => {
				if (this.#run !== run) return;
				this.#sub = null;
				// A lost connection reads again once it is open; anything else leaves the rows
				// the operations give.
				if (why.kind === 'lost') this.#waiting = true;
			},
		};
		client.subscribe('session.watch', {}, handlers).then(
			(sub) => {
				if (this.#run !== run) {
					this.#unsubscribe(client, sub);
					return;
				}
				this.#sub = sub;
				// Read after the subscription opened: it replaces the list, the events merge by id.
				const next = new Map<string, Listed>();
				let moved: string | null = null;
				for (const s of sub.result.sessions) {
					next.set(s.id, listedFrom(s));
					this.#named(s.id, s.name);
					// Listed now, so not ended, whatever an earlier read concluded.
					this.#gone.delete(s.id);
					this.#track(s.id);
					// A followed session that moved while the watch could not see still moves the selection.
					if (this.#following === s.id && s.workspace !== undefined && s.workspace !== this.#listed.get(s.id)?.workspace) moved = s.workspace;
				}
				// A session listed before and not now ended while the watch could not see.
				for (const id of this.#listed.keys()) {
					if (next.has(id)) continue;
					this.#gone.add(id);
					if (this.#following === id) this.#following = null;
				}
				this.#listed = next;
				if (moved !== null) this.#onFollow?.(moved);
				this.#changed();
			},
			(err: unknown) => {
				if (this.#run !== run) return;
				if (err instanceof ConnectionError && err.kind === 'lost') this.#waiting = true;
				else console.warn('session.watch:', failureText(failure(err)));
			},
		);
	}

	#event(frame: EventFrame): void {
		const ev = frame.event;
		if (ev.topic === TOPIC_GAP) {
			// Events were lost: the list is read again (session.watch's contract).
			this.#begin();
			return;
		}
		if (ev.topic !== TOPIC_STATE) return;
		const p = ev.payload as StateEvent;
		this.#onState?.();
		if (this.#gone.has(p.id)) return;
		if (p.ended) {
			this.#listed.delete(p.id);
			this.#gone.add(p.id);
			if (this.#following === p.id) this.#following = null;
		} else {
			const prev = this.#listed.get(p.id);
			this.#listed.set(p.id, listedFrom(p));
			this.#track(p.id);
			this.#named(p.id, p.name);
			if (this.#following === p.id && p.workspace !== undefined && p.workspace !== prev?.workspace) this.#onFollow?.(p.workspace);
		}
		this.#changed();
	}

	#named(id: string, name: string | undefined): void {
		const t = this.#track(id);
		if (name) t.name = name;
	}

	#release(): void {
		const sub = this.#sub;
		this.#sub = null;
		if (sub && this.#client) this.#unsubscribe(this.#client, sub);
	}

	#unsubscribe(client: SessionsClient, sub: Subscription): void {
		sub.stop();
		client.call('events.unsubscribe', { subscriptionId: sub.subscriptionId }).catch(() => {
			// The session may be gone; its subscriptions went with it.
		});
	}

	#observe(): void {
		const now = this.#now();
		const ops = this.#source.operations;
		// Oldest first, so the newest ended operation decides the row's last result.
		for (let i = ops.length - 1; i >= 0; i--) {
			const op = ops[i]!;
			const key = forRows(op);
			if (key === null) continue;
			const t = this.#track(key);
			if (op.session?.name) t.name = op.session.name;
			if (!isTerminal(op.state) || this.#ended.has(op.id)) continue;
			this.#ended.add(op.id);
			t.lastEndAt = now;
			t.lastResult = op.state;
			if (this.#selected !== key) t.unread = true;
		}
	}

	#changed(): void {
		for (const l of [...this.#listeners]) l();
	}
}

/**
 * The row an operation belongs to; null for one no row shows. The user's `ui` and `cli`
 * commands are "You" (their reads are not shown); an MCP call belongs to its session.
 */
export function keyOf(op: Operation): string | null {
	// Opening a project is connecting, not work: an agent's bound open runs before its
	// workspace is open and would place it under "No project", and it would mark the row
	// unread (ADR-0048 §3: an agent that has run nothing is not listed).
	if (op.capability === 'workspace.open') return null;
	// The hook's record of an agent's command is shown in Activity, never as the user's
	// (ADR-0063 §6). Joined to an MCP session (ADR-0095 §2) it has that session's key, so
	// Activity shows it under that agent and narrows to it; the panel's rows and unread
	// marks do not count it (see forRows), as before. Else it has no key.
	if (op.capability === 'agent.recordCommand') return op.session && isAgentOp(op) ? op.session.id : null;
	// A call from an agent's hook or shell was joined to its MCP session (ADR-0095): it
	// carries the session's ref, with its name, whatever connection it came in on.
	if (op.session && isAgentOp(op)) return op.session.id;
	if (op.client === 'ui' || op.client === 'cli') {
		// A read, or one of the tab's own subscriptions (a streaming watch), is not activity.
		const info = capabilityInfo[op.capability as CapabilityName] as { readOnly: boolean; statefulness: string } | undefined;
		return info?.readOnly === true || info?.statefulness === 'streaming' ? null : YOU;
	}
	return op.client === 'mcp' && op.session ? op.session.id : null;
}

/** The key the panel's rows and unread marks count an operation under: keyOf, but a hook's record counts for none. */
function forRows(op: Operation): string | null {
	return op.capability === 'agent.recordCommand' ? null : keyOf(op);
}

/**
 * An agent's operation: its session has a name, whatever client carried it. An MCP call
 * names its session; so does a CLI call joined to one (ADR-0095 §2: an agent's `droidyard
 * trash`). The user's own calls carry none, or a WebSocket session with no name.
 */
export function isAgentOp(op: Pick<Operation, 'session'>): boolean {
	return !!op.session?.name;
}

/** Labels by session name, a repeated name numbered in the order the sessions were first seen. */
function labelsFor(order: readonly string[], nameOf: (key: string) => string | undefined): Map<string, string> {
	const labels = new Map<string, string>();
	const used = new Map<string, number>();
	for (const key of order) {
		if (key === YOU) continue;
		const name = nameOf(key) ?? 'agent';
		const n = (used.get(name) ?? 0) + 1;
		used.set(name, n);
		labels.set(key, n === 1 ? name : `${name} ${n}`);
	}
	return labels;
}

/** The agent row that holds the linked worktree with this workspace id, if one does. */
export function holderOf(rows: readonly AgentRow[], workspace: string): AgentRow | undefined {
	return rows.find((r) => r.occupies === workspace);
}

/**
 * What a session row says it occupies: the worktree's name (the id where the panel does not
 * know it) and the serials of its devices, comma separated; '' when it holds nothing.
 */
export function occupiesText(a: Pick<AgentRow, 'occupies' | 'devices'>, nameOf: (workspace: string) => string | undefined): string {
	const parts: string[] = [];
	if (a.occupies !== null) parts.push(nameOf(a.occupies) ?? a.occupies);
	parts.push(...a.devices);
	return parts.join(', ');
}

export interface DeviceHold {
	serial: string;
	/** The holding session's actor label. */
	holder: string;
	idle: boolean;
}

/** The devices the sessions hold now, by serial, from the live rows; the holder reads as `actorLabel` says. */
export function deviceHolds(rows: readonly AgentRow[], actorLabel: (id: string) => string): DeviceHold[] {
	const out: DeviceHold[] = [];
	for (const r of rows) {
		for (const serial of r.devices) out.push({ serial, holder: actorLabel(r.key), idle: r.idle });
	}
	return out.sort((a, b) => a.serial.localeCompare(b.serial));
}

/** "in use by claude", "in use by claude (idle)": the line a held device wears. */
export function inUseText(h: Pick<DeviceHold, 'holder' | 'idle'>): string {
	return h.idle ? `in use by ${h.holder} (idle)` : `in use by ${h.holder}`;
}

/** "held by claude","held by claude (idle)": the line a worktree or a device wears while another session holds it. */
export function heldByText(holder: string, idle: boolean): string {
	return idle ? `held by ${holder} (idle)` : `held by ${holder}`;
}

/** "12 s", "3 m 05 s", "1 h 02 m": a running operation's age. */
export function duration(ms: number): string {
	const s = Math.max(0, Math.floor(ms / 1000));
	if (s < 60) return `${s} s`;
	const m = Math.floor(s / 60);
	if (m < 60) return `${m} m ${String(s % 60).padStart(2, '0')} s`;
	return `${Math.floor(m / 60)} h ${String(m % 60).padStart(2, '0')} m`;
}
