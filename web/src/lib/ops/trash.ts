// The trash in Activity (plan m2s step 8, ADR-0096, ADR-0105): `operation.watch` lists operations from
// the tab's load on, so a delete made before it has no row and no Restore. `file.trash` lists
// the workspace's trash; the entries no operation of the store accounts for are shown as
// "deleted" rows with Restore, under the agent or You by the entry's `by`.
import type { Params, Result } from '../api/capabilities.gen';
import { CapabilityError } from '../api/client';
import type { Operation } from './activity';

export type TrashEntry = Result<'file.trash'>['entries'][number];

export interface TrashClient {
	call(name: 'file.trash', params: Params<'file.trash'>, opts: { workspace: string }): Promise<Result<'file.trash'>>;
	call(
		name: 'file.restore',
		params: Params<'file.restore'>,
		opts: { workspace: string },
	): Promise<Result<'file.restore'>>;
}

/** What a Restore of one entry came to. `gone`: the entry was not in the trash any more. */
export type TrashRowState =
	| { kind: 'working' }
	| { kind: 'restored'; path: string }
	| { kind: 'gone' }
	| { kind: 'failed'; code: string; message: string };

/** What the row says when the entry is already gone (FILE_NOT_IN_TRASH). */
export const GONE_TEXT = 'Already gone from the trash: it was restored, removed after 7 days, or is past the size limit.';

/** How far an entry's time may lie outside its operation's span: the two clocks are the daemon's, rounded. */
const SLACK_MS = 1000;

/**
 * The entries no `file.delete` operation of this workspace accounts for: those whose
 * `deletedAt` falls outside every such operation's span (its start to its start plus its
 * duration; a queued or running one has no end yet). An entry inside a span has the
 * operation's own row, which offers Restore, and is not shown twice. An operation that ended
 * failed, cancelled or `unseen` accounts for nothing: its row cannot restore, so its
 * entries show here. Newest first, as `entries` came.
 */
export function unaccounted(
	entries: readonly TrashEntry[],
	operations: readonly Operation[],
	workspaceId: string,
): TrashEntry[] {
	const spans: [number, number][] = [];
	for (const o of operations) {
		if (o.capability !== 'file.delete' || o.workspace !== workspaceId) continue;
		// Only an operation whose row can restore accounts for its entries (completed, or still going).
		if (o.state !== 'completed' && o.state !== 'queued' && o.state !== 'running') continue;
		const start = Date.parse(o.startedAt);
		if (Number.isNaN(start)) continue;
		const live = o.state === 'queued' || o.state === 'running';
		spans.push([start - SLACK_MS, live ? Infinity : start + (o.durationMs ?? 0) + SLACK_MS]);
	}
	return entries.filter((e) => {
		const at = Date.parse(e.deletedAt);
		return Number.isNaN(at) || !spans.some(([from, to]) => at >= from && at <= to);
	});
}

/** Who deleted, as a row says it: a name makes it an agent's (ADR-0095); no `by`, or no name, is You. */
export function deletedBy(e: Pick<TrashEntry, 'by'>): { label: string; you: boolean } {
	const name = e.by?.name;
	if (name) return { label: name, you: false };
	if (e.by?.client === 'mcp') return { label: 'agent', you: false };
	return { label: 'You', you: true };
}

/**
 * Whether the session whose own name is `name` (undefined: it gave none) deleted this entry.
 * `by` carries only the session's name, so the match is by that raw name, never by a numbered
 * label such as "claude-code 2", which no entry carries.
 */
export function deletedBySession(e: Pick<TrashEntry, 'by'>, name: string | undefined): boolean {
	const who = deletedBy(e);
	return !who.you && who.label === (name || 'agent');
}

/** The text of one row's outcome. */
export function stateText(s: TrashRowState): string {
	switch (s.kind) {
		case 'working':
			return 'Restoring…';
		case 'restored':
			return `Restored to ${s.path}`;
		case 'gone':
			return GONE_TEXT;
		case 'failed':
			return `${s.code}: ${s.message}`;
	}
}

export class TrashModel {
	#client: TrashClient;
	#root: () => string;
	#entries: readonly TrashEntry[] = [];
	#error: string | null = null;
	#states = new Map<string, TrashRowState>();
	#listeners = new Set<() => void>();
	#loads = 0;

	constructor(client: TrashClient, root: () => string) {
		this.#client = client;
		this.#root = root;
	}

	/** The workspace's trash as the daemon last said, newest first. */
	get entries(): readonly TrashEntry[] {
		return this.#entries;
	}

	/** Why the last load failed, as text; null when it did not. */
	get error(): string | null {
		return this.#error;
	}

	stateOf(id: string): TrashRowState | undefined {
		return this.#states.get(id);
	}

	subscribe(listener: () => void): () => void {
		this.#listeners.add(listener);
		return () => {
			this.#listeners.delete(listener);
		};
	}

	/** Reads the trash. Nothing is asked until the project's root is known; the last answer asked wins. */
	async load(): Promise<void> {
		const root = this.#root().trim();
		if (root === '') return;
		const n = ++this.#loads;
		try {
			const res = await this.#client.call('file.trash', {}, { workspace: root });
			if (n !== this.#loads) return;
			this.#entries = res.entries;
			this.#error = null;
		} catch (err) {
			if (n !== this.#loads) return;
			this.#error = err instanceof CapabilityError ? `${err.code}: ${err.message}` : err instanceof Error ? err.message : 'the trash could not be read';
		}
		this.#changed();
	}

	/** Puts one entry back. A second click while it works, or once it is back, is ignored. */
	async restore(id: string): Promise<void> {
		const now = this.#states.get(id);
		if (now?.kind === 'working' || now?.kind === 'restored') return;
		this.#set(id, { kind: 'working' });
		try {
			const out = await this.#client.call('file.restore', { trashIds: [id] }, { workspace: this.#root().trim() });
			const item = out.items.find((i) => i.trashId === id);
			if (item === undefined) {
				this.#set(id, { kind: 'failed', code: 'UNKNOWN', message: 'the daemon did not answer for this entry' });
			} else if (item.code === undefined) {
				this.#set(id, { kind: 'restored', path: item.path ?? '' });
			} else if (item.code === 'FILE_NOT_IN_TRASH') {
				this.#set(id, { kind: 'gone' });
			} else {
				this.#set(id, { kind: 'failed', code: item.code, message: item.message ?? '' });
			}
		} catch (err) {
			if (err instanceof CapabilityError) this.#set(id, { kind: 'failed', code: err.code, message: err.message });
			else this.#set(id, { kind: 'failed', code: 'UNKNOWN', message: err instanceof Error ? err.message : 'restore failed' });
		}
	}

	#set(id: string, s: TrashRowState): void {
		this.#states.set(id, s);
		this.#changed();
	}

	#changed(): void {
		for (const l of [...this.#listeners]) l();
	}
}
