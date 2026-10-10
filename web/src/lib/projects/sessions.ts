// The agent rows of the projects panel (plan m2d D30, ADR-0048), derived from the activity
// store's operations. A row exists because its session ran something: a connected agent
// that has run nothing since the tab loaded is not listed (Q2), and a row leaves the panel
// AGENT_ROW_TTL_MS after its last terminal event (D26).
//
//   - MCP sessions are rows, labelled by `session.name` (a second session with the same
//     name gets " 2", in the order seen; no name: "agent"). The user's `ui` and `cli`
//     commands are one "You" row, without read-only calls (the tab's own reads, m2b D16).
//   - A session's workspace is the one on its latest operation that carries one; a session
//     without one sits in the "No project" group (workspace null). "You" follows the
//     workspace of its latest operation, which has no session binding.
//   - A row runs while any of its operations is not terminal. Unread is a terminal event
//     since the row was last selected, per tab, in memory only.
//   - The rail's count and spinner follow agent rows only, never "You".
import { capabilityInfo, type CapabilityName } from '../api/capabilities.gen';
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

export class SessionsModel {
	#source: SessionsSource;
	#now: () => number;
	#tracks = new Map<string, Track>();
	#ended = new Set<string>(); // operations already counted as ended
	#selected: string | null = null;
	#off: (() => void) | null = null;
	#listeners = new Set<() => void>();
	#order = 0;

	constructor(source: SessionsSource, now: () => number = Date.now) {
		this.#source = source;
		this.#now = now;
	}

	start(): void {
		if (this.#off) return;
		this.#observe();
		this.#off = this.#source.subscribe(() => {
			this.#observe();
			this.#changed();
		});
	}

	stop(): void {
		this.#off?.();
		this.#off = null;
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
			// Opening a project is connecting, not work: an agent's bound open runs before its
			// workspace is open and would place it under "No project" (ADR-0048 §3: an agent
			// that has run nothing is not listed).
			if (key === null || op.capability === 'workspace.open') continue;
			let list = bySession.get(key);
			if (!list) bySession.set(key, (list = []));
			list.push(op);
		}
		const seen = [...bySession.keys()]
			.map((k) => this.#tracks.get(k))
			.filter((t): t is Track => t !== undefined)
			.sort((a, b) => a.order - b.order);
		const labels = labelsFor(seen.map((t) => t.key), bySession);

		const rows: AgentRow[] = [];
		for (const [key, list] of bySession) {
			const track = this.#tracks.get(key);
			if (!track) continue;
			const live = list.filter((o) => !isTerminal(o.state));
			if (live.length === 0 && now - track.lastEndAt >= AGENT_ROW_TTL_MS) continue;
			rows.push({
				key,
				label: key === YOU ? 'You' : (labels.get(key) ?? 'agent'),
				you: key === YOU,
				workspace:
					key === YOU
						? (list.find((o) => o.workspace !== null)?.workspace ?? null)
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

	#observe(): void {
		const now = this.#now();
		const ops = this.#source.operations;
		// Oldest first, so the newest ended operation decides the row's last result.
		for (let i = ops.length - 1; i >= 0; i--) {
			const op = ops[i]!;
			const key = keyOf(op);
			if (key === null) continue;
			let t = this.#tracks.get(key);
			if (!t) this.#tracks.set(key, (t = { key, order: this.#order++, lastEndAt: 0, lastResult: null, unread: false }));
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
	if (op.client === 'ui' || op.client === 'cli') {
		// A read, or one of the tab's own subscriptions (a streaming watch), is not activity.
		const info = capabilityInfo[op.capability as CapabilityName] as { readOnly: boolean; statefulness: string } | undefined;
		return info?.readOnly === true || info?.statefulness === 'streaming' ? null : YOU;
	}
	return op.client === 'mcp' && op.session ? op.session.id : null;
}

/** Labels by session name, a repeated name numbered in the order the sessions were first seen. */
function labelsFor(order: readonly string[], bySession: ReadonlyMap<string, Operation[]>): Map<string, string> {
	const labels = new Map<string, string>();
	const used = new Map<string, number>();
	for (const key of order) {
		if (key === YOU) continue;
		const name = bySession.get(key)?.find((o) => o.session?.name)?.session?.name ?? 'agent';
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
