// Restore from Activity (plan m2j step 10, Q3): the UI cannot undo a delete it did not
// make, because Undo is the tab's own stack. Every completed `file.delete` row, whatever its
// client, offers Restore. Only on click does the model read the operation's result
// (`operation.wait`, timeoutMs 0) and call `file.restore` with the done items' trash ids.
// An `OPERATION_NOT_FOUND` answer means the daemon no longer keeps the result (15 minutes
// or 200 operations); the row then says so and points at the CLI.
import type { Params, Result } from '../api/capabilities.gen';
import { CapabilityError } from '../api/client';
import type { Block } from './blocks';

export interface RestoreClient {
	call(name: 'operation.wait', params: Params<'operation.wait'>): Promise<Result<'operation.wait'>>;
	call(
		name: 'file.restore',
		params: Params<'file.restore'>,
		opts: { workspace: string },
	): Promise<Result<'file.restore'>>;
}

export interface RestoredItem {
	/** The path as the delete was asked. */
	path: string;
	/** Where it is now; present when restored. */
	restoredTo?: string;
	/** Present when this item failed: the daemon's code, shown, never swallowed. */
	code?: string;
	message?: string;
}

export type RestoreState =
	| { kind: 'working' }
	| { kind: 'done'; items: RestoredItem[] }
	| { kind: 'nothing' }
	| { kind: 'expired' }
	| { kind: 'failed'; code: string; message: string };

/** What the row says when the result is past retention. */
export const EXPIRED_TEXT =
	'The result of this delete is past retention (the daemon keeps 15 minutes or 200 operations). ' +
	'Restore from the CLI: droidyard call file.restore with the trash ids that droidyard call file.delete printed.';

/** Restore is offered on a completed `file.delete` of this workspace, whatever its client. */
export function canRestore(b: Pick<Block, 'capability' | 'tone' | 'workspace'>, workspaceId: string): boolean {
	return b.capability === 'file.delete' && b.tone === 'success' && b.workspace === workspaceId;
}

export class RestoreModel {
	#client: RestoreClient;
	#root: () => string;
	#states = new Map<string, RestoreState>();
	#listeners = new Set<() => void>();

	constructor(client: RestoreClient, root: () => string) {
		this.#client = client;
		this.#root = root;
	}

	stateOf(operationId: string): RestoreState | undefined {
		return this.#states.get(operationId);
	}

	subscribe(listener: () => void): () => void {
		this.#listeners.add(listener);
		return () => {
			this.#listeners.delete(listener);
		};
	}

	/** Reads the delete's result and restores its done items. A second click while working is ignored. */
	async restore(operationId: string): Promise<void> {
		const now = this.#states.get(operationId);
		if (now?.kind === 'working' || now?.kind === 'done') return;
		this.#set(operationId, { kind: 'working' });
		try {
			// The client hands back the envelope's `result`, which for operation.wait is the waited
			// operation's own result (api/client.test.ts), here file.delete's.
			const waited: unknown = await this.#client.call('operation.wait', { operationId, timeoutMs: 0 });
			const res = waited as Partial<Result<'file.delete'>> | undefined;
			const done = (res?.items ?? []).filter(
				(i): i is typeof i & { trashId: string } => i.trashId !== undefined && i.code === undefined,
			);
			if (done.length === 0) {
				this.#set(operationId, { kind: 'nothing' });
				return;
			}
			const out = await this.#client.call(
				'file.restore',
				{ trashIds: done.map((i) => i.trashId) as [string, ...string[]] },
				{ workspace: this.#root().trim() },
			);
			const pathOf = new Map(done.map((i) => [i.trashId, i.path]));
			this.#set(operationId, {
				kind: 'done',
				items: out.items.map((i) => ({
					path: pathOf.get(i.trashId) ?? i.trashId,
					restoredTo: i.code === undefined ? i.path : undefined,
					code: i.code,
					message: i.message,
				})),
			});
		} catch (err) {
			if (err instanceof CapabilityError && err.code === 'OPERATION_NOT_FOUND') {
				this.#set(operationId, { kind: 'expired' });
			} else if (err instanceof CapabilityError) {
				this.#set(operationId, { kind: 'failed', code: err.code, message: err.message });
			} else {
				this.#set(operationId, { kind: 'failed', code: 'UNKNOWN', message: err instanceof Error ? err.message : 'restore failed' });
			}
		}
	}

	#set(id: string, s: RestoreState): void {
		this.#states.set(id, s);
		for (const l of [...this.#listeners]) l();
	}
}

/** One line per item, as text. */
export function itemText(i: RestoredItem): string {
	return i.code === undefined
		? `${i.path}: restored${i.restoredTo !== undefined && i.restoredTo !== i.path ? ` to ${i.restoredTo}` : ''}`
		: `${i.path}: ${i.code}${i.message ? `: ${i.message}` : ''}`;
}
