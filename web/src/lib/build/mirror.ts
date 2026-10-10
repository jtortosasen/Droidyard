// The build mirror (plan m2b D11): *building* is a non-terminal `build.assemble` or
// `build.run` of this workspace, whoever started it, read only from the activity store.
// The panel shows its state and client kind and offers Cancel through `operation.cancel`
// (any session may cancel any operation). It never calls `operation.wait` on an id the UI
// did not start: that would take an agent's full result from it.
//
// Both the build panel and the tasks panel use it, so they show one state.
import type { OperationProgress, Params, Result } from '../api/capabilities.gen';
import type { CallOptions } from '../api/client';
import type { Operation } from '../ops/activity';
import { failure, failureText } from './failure';

/** The capabilities that build; the workspace's reload capabilities are never named here (D13). */
export const BUILD_CAPABILITIES = ['build.assemble', 'build.run'] as const;

export interface MirrorActivity {
	active(predicate: (op: Operation) => boolean): Operation[];
	subscribe(listener: () => void): () => void;
}

export interface MirrorProgress {
	progress(operationId: string): OperationProgress | undefined;
	subscribe(listener: () => void): () => void;
}

export interface MirrorClient {
	call(name: 'operation.cancel', params: Params<'operation.cancel'>, opts?: CallOptions): Promise<Result<'operation.cancel'>>;
}

export interface Building {
	operationId: string;
	capability: string;
	/** cli, mcp or ui. */
	client: string | undefined;
	/** The daemon session that made the call (ADR-0048); the toolbar names an agent's build by it. */
	session?: Operation['session'];
	startedAt: string;
	state: 'queued' | 'running';
	/**
	 * What the build waited behind when it was recorded. The daemon does not send it again
	 * when the holder changes, so it is not "current": show it as "queued behind another build".
	 */
	blockedBy?: Operation['blockedBy'];
	progress?: OperationProgress;
}

export interface MirrorView {
	building: Building | null;
	/** The last Cancel that failed, as text. */
	cancelError: string | null;
}

export class BuildMirror {
	#activity: MirrorActivity;
	#progress: MirrorProgress;
	#client: MirrorClient;
	#workspaceId: () => string | null;
	#listeners = new Set<() => void>();
	#view: MirrorView | null = null;
	#cancelError: string | null = null;
	#offs: (() => void)[] = [];

	constructor(activity: MirrorActivity, progress: MirrorProgress, client: MirrorClient, workspaceId: () => string | null) {
		this.#activity = activity;
		this.#progress = progress;
		this.#client = client;
		this.#workspaceId = workspaceId;
		this.#offs.push(activity.subscribe(() => this.changed()));
		this.#offs.push(progress.subscribe(() => this.changed()));
	}

	get view(): MirrorView {
		this.#view ??= { building: this.#building(), cancelError: this.#cancelError };
		return this.#view;
	}

	subscribe(listener: () => void): () => void {
		this.#listeners.add(listener);
		return () => {
			this.#listeners.delete(listener);
		};
	}

	/** Cancels the build shown; a no-op when none is. */
	async cancel(): Promise<void> {
		const b = this.view.building;
		if (b === null) return;
		this.#cancelError = null;
		try {
			await this.#client.call('operation.cancel', { operationId: b.operationId });
		} catch (e) {
			this.#cancelError = failureText(failure(e));
		}
		this.changed();
	}

	/** The workspace id changed (a panel calls it): the view is read again. */
	changed(): void {
		this.#view = null;
		for (const l of [...this.#listeners]) l();
	}

	dispose(): void {
		for (const off of this.#offs) off();
		this.#offs = [];
	}

	#building(): Building | null {
		const id = this.#workspaceId();
		if (id === null) return null;
		const op = this.#activity
			.active((o) => (BUILD_CAPABILITIES as readonly string[]).includes(o.capability) && o.workspace === id)
			// The store lists newest first; the one running, if any, is the one to show.
			.sort((a, b) => (a.state === b.state ? 0 : a.state === 'running' ? -1 : 1))[0];
		if (op === undefined || (op.state !== 'queued' && op.state !== 'running')) return null;
		return {
			operationId: op.id,
			capability: op.capability,
			client: op.client,
			session: op.session,
			startedAt: op.startedAt,
			state: op.state,
			blockedBy: op.blockedBy,
			progress: this.#progress.progress(op.id),
		};
	}
}
