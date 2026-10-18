// The agent rows of the projects panel (plan m2d D30, ADR-0048, ADR-0083). A row exists
// because its MCP session is connected (`session.watch`: an idle session is a row, and
// several may sit in one worktree) or because it ran something the activity store saw; a
// session the watch does not list leaves AGENT_ROW_TTL_MS after its last terminal event
// (D26), and one the watch reports ended leaves at once when idle, else after its last
// operation.
//
//   - MCP sessions are rows, labelled by `session.name` (a second session with the same
//     name gets " 2", in the order seen; no name: "agent"). The user's `ui` and `cli`
//     commands are one "You" row, without read-only calls (the tab's own reads, m2b D16).
//   - A listed session's workspace is the one the watch says; one the watch does not list
//     takes the one on its latest operation that carries one; a session without one sits in
//     the "No project" group (workspace null). "You" follows the workspace of its latest
//     operation, which has no session binding.
//   - A row runs while any of its operations is not terminal. Unread is a terminal event
//     since the row was last selected, per tab, in memory only.
//   - The rail's count and spinner follow agent rows only, never "You".
//   - The UI follows a session and never moves it (ADR-0083 §3): `follow` names the session
//     whose moves the selection takes; `unfollow` is a selection made from a project row.
import { capabilityInfo, eventTopics, type CapabilityName, type EventPayload, type Params, type Result } from '../api/capabilities.gen';
import { ConnectionError, type ConnectionState, type EventFrame, type SubscribeHandlers, type Subscription } from '../api/client';
import { failure, failureText } from '../build/failure';
import { isTerminal, type Operation, type OperationState } from '../ops/activity';

/** Working default (plan D26): an agent row leaves the panel this long after its last terminal event. */
export const AGENT_ROW_TTL_MS = 30 * 60 * 1000;

/** The row of the user's own commands. */
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
	/** The session id, or YOU. */
	key: string;
	label: string;
	you: boolean;
	/** The project the row belongs to; null is the "No project" group. */
	workspace: string | null;
	running: boolean;
	/** The state of the newest operation that ended; null before any. */
	result: OperationState | null;
	unread: boolean;
	/** Not terminal, oldest first. */
	operations: RunningOperation[];
}

export interface SessionsView {
	/** Running rows first; within each, the row with the newest activity first. */
	rows: AgentRow[];
	/** Agent rows (never "You") with an operation not terminal: the rail's count. */
	runningAgents: number;
}

interface Track {
	key: string;
	order: number; // first seen: duplicate names are numbered in this order
	lastEndAt: number; // the browser's time of the newest terminal event seen; 0 before any
	lastResult: OperationState | null;
	unread: boolean;
}

/** The daemon's side `session.watch` needs (ADR-0083 §1). */
export interface SessionsClient {
	call(name: 'events.unsubscribe', params: Params<'events.unsubscribe'>): Promise<Result<'events.unsubscribe'>>;
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
}

/** A connected MCP session, as `session.watch` last said. */
interface Listed {
	name?: string;
	workspace?: string;
}

const TOPIC_STATE = eventTopics['session.watch'];
const TOPIC_GAP = 'events.gap';
type StateEvent = EventPayload<'session.watch', 'state'>;

export class SessionsModel {
	#source: SessionsSource;
	#now: () => number;
	#client: SessionsClient | undefined;
	#onFollow: ((workspace: string) => void) | undefined;
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
	}

	unfollow(): void {
		this.#following = null;
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

	/** Computed from the clock every time, so the 30-minute rule needs no timer of its own. */
	get view(): SessionsView {
		const now = this.#now();
		const ops = this.#source.operations; // newest first
		const bySession = new Map<string, Operation[]>();
		for (const op of ops) {
			const key = keyOf(op);
			if (key === null) continue;
			let list = bySession.get(key);
			if (!list) bySession.set(key, (list = []));
			list.push(op);
		}
		const keys = new Set([...bySession.keys(), ...this.#listed.keys()]);
		const seen = [...keys]
			.map((k) => this.#tracks.get(k))
			.filter((t): t is Track => t !== undefined)
			.sort((a, b) => a.order - b.order);
		const nameOf = (k: string) => this.#listed.get(k)?.name ?? bySession.get(k)?.find((o) => o.session?.name)?.session?.name;
		const labels = labelsFor(seen.map((t) => t.key), nameOf);

		const rows: AgentRow[] = [];
		for (const key of keys) {
			const track = this.#tracks.get(key);
			if (!track) continue;
			const list = bySession.get(key) ?? [];
			const live = list.filter((o) => !isTerminal(o.state));
			const listed = this.#listed.get(key);
			// Ended: it leaves at once when idle, else after its last operation. Not listed by the
			// watch: it ages out as before. Listed and connected: it stays, idle or not.
			if (live.length === 0 && (this.#gone.has(key) || (listed === undefined && now - track.lastEndAt >= AGENT_ROW_TTL_MS))) continue;
			rows.push({
				key,
				label: key === YOU ? 'You' : (labels.get(key) ?? 'agent'),
				you: key === YOU,
				workspace:
					key === YOU
						? (list.find((o) => o.workspace !== null)?.workspace ?? null)
						: listed !== undefined
							? (listed.workspace ?? null)
							: (list.find((o) => o.session?.workspace !== undefined)?.session?.workspace ?? null),
				running: live.length > 0,
				result: track.lastResult,
				unread: track.unread,
				operations: live
					.map((o) => ({ id: o.id, capability: o.capability, startedAt: o.startedAt }))
					.reverse(),
			});
		}
		const activity = (r: AgentRow) => this.#tracks.get(r.key)?.lastEndAt ?? 0;
		rows.sort((a, b) => Number(b.running) - Number(a.running) || activity(b) - activity(a) || a.label.localeCompare(b.label));
		return { rows, runningAgents: rows.filter((r) => r.running && !r.you).length };
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
					next.set(s.id, { ...(s.name !== undefined ? { name: s.name } : {}), ...(s.workspace !== undefined ? { workspace: s.workspace } : {}) });
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
		if (this.#gone.has(p.id)) return;
		if (p.ended) {
			this.#listed.delete(p.id);
			this.#gone.add(p.id);
			if (this.#following === p.id) this.#following = null;
		} else {
			const prev = this.#listed.get(p.id);
			this.#listed.set(p.id, { ...(p.name !== undefined ? { name: p.name } : {}), ...(p.workspace !== undefined ? { workspace: p.workspace } : {}) });
			this.#track(p.id);
			if (this.#following === p.id && p.workspace !== undefined && p.workspace !== prev?.workspace) this.#onFollow?.(p.workspace);
		}
		this.#changed();
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
			const key = keyOf(op);
			if (key === null) continue;
			const t = this.#track(key);
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
	// The hook's record of an agent's command is shown in Activity but is no one's session
	// (ADR-0063 §6): no "You" row lights up, and no agent row appears.
	if (op.capability === 'agent.recordCommand') return null;
	if (op.client === 'ui' || op.client === 'cli') {
		// A read, or one of the tab's own subscriptions (a streaming watch), is not activity.
		const info = capabilityInfo[op.capability as CapabilityName] as { readOnly: boolean; statefulness: string } | undefined;
		return info?.readOnly === true || info?.statefulness === 'streaming' ? null : YOU;
	}
	return op.client === 'mcp' && op.session ? op.session.id : null;
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

/** "12 s", "3 m 05 s", "1 h 02 m": a running operation's age. */
export function duration(ms: number): string {
	const s = Math.max(0, Math.floor(ms / 1000));
	if (s < 60) return `${s} s`;
	const m = Math.floor(s / 60);
	if (m < 60) return `${m} m ${String(s % 60).padStart(2, '0')} s`;
	return `${Math.floor(m / 60)} h ${String(m % 60).padStart(2, '0')} m`;
}
