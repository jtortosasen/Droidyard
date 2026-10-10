// The workspace the panels work on (plan m2b D10 and §Changes). The root is typed once and
// kept in localStorage (a view preference); `workspace.open` returns the id, and every
// event and operation is matched on that id. No capability lists the open workspaces, so
// the panel knows only what it opened and what `workspace.lifecycle` tells it afterwards:
// a workspace another client opens or closes is shown, and when the open one is closed by
// another client the panel says so and offers to reopen it. It never reopens by itself
// after that (a closed workspace is a decision, mcp.md).
import type { Params, Result } from '../api/capabilities.gen';
import { CapabilityError, type CallOptions } from '../api/client';
import { failure, type Failure } from './failure';
import type { EventsHub, LifecycleEvent } from './hub';

export const ROOT_KEY = 'droidyard.workspaceRoot';

type Calls = 'workspace.open' | 'workspace.describe';

export interface WorkspaceClient {
	call<N extends Calls>(name: N, params: Params<N>, opts?: CallOptions): Promise<Result<N>>;
}

/** The slice of localStorage used; every access is guarded, the panel works without it. */
export interface RootStorage {
	getItem(key: string): string | null;
	setItem(key: string, value: string): void;
}

export interface WorkspaceInfo {
	id: string;
	name: string;
	state: string;
	trusted: boolean;
	degradedBy?: Failure;
}

export interface WorkspaceView {
	root: string;
	/** The workspace this panel opened; null before it and while it is not open. */
	info: WorkspaceInfo | null;
	opening: boolean;
	error: Failure | null;
	/** The open workspace was closed by another client: the panel offers to reopen. */
	closedByOther: boolean;
	/** Workspaces seen on workspace.lifecycle, by id, with their last state (opened by anyone). */
	seen: readonly { id: string; state: string }[];
}

export class WorkspaceModel {
	#client: WorkspaceClient;
	#storage: RootStorage | null;
	#root = '';
	#info: WorkspaceInfo | null = null;
	#opening = false;
	#error: Failure | null = null;
	#closed = false;
	#seen = new Map<string, string>();
	#listeners = new Set<() => void>();
	#view: WorkspaceView | null = null;
	#offs: (() => void)[] = [];

	constructor(client: WorkspaceClient, hub: Pick<EventsHub, 'onLifecycle' | 'onResync'>, storage: RootStorage | null) {
		this.#client = client;
		this.#storage = storage;
		try {
			this.#root = storage?.getItem(ROOT_KEY) ?? '';
		} catch {
			// Blocked storage: the root is typed again.
		}
		this.#offs.push(hub.onLifecycle((e) => this.#lifecycle(e)));
		this.#offs.push(hub.onResync(() => void this.#resync()));
	}

	get root(): string {
		return this.#root;
	}

	/** The id of the open workspace; null when none is (or it was closed by another client). */
	get id(): string | null {
		return this.#info !== null && !this.#closed ? this.#info.id : null;
	}

	get view(): WorkspaceView {
		this.#view ??= {
			root: this.#root,
			info: this.#info,
			opening: this.#opening,
			error: this.#error,
			closedByOther: this.#closed,
			seen: [...this.#seen].map(([id, state]) => ({ id, state })),
		};
		return this.#view;
	}

	subscribe(listener: () => void): () => void {
		this.#listeners.add(listener);
		return () => {
			this.#listeners.delete(listener);
		};
	}

	dispose(): void {
		for (const off of this.#offs) off();
		this.#offs = [];
	}

	setRoot(root: string): void {
		this.#root = root;
		try {
			this.#storage?.setItem(ROOT_KEY, root);
		} catch {
			// Blocked storage: the root lives for this tab only.
		}
		this.#changed();
	}

	/** `workspace.open` for the typed root; also the "reopen" after another client closed it. */
	async open(): Promise<void> {
		const root = this.#root.trim();
		if (root === '' || this.#opening) return;
		this.#opening = true;
		this.#error = null;
		this.#changed();
		try {
			const r = await this.#client.call('workspace.open', {}, { workspace: root });
			this.#info = {
				id: r.id,
				name: r.name,
				state: r.state,
				trusted: r.trusted,
				degradedBy: r.degradedBy ? { code: r.degradedBy.code, message: r.degradedBy.message, nextStep: r.degradedBy.nextStep } : undefined,
			};
			this.#closed = false;
			this.#seen.set(r.id, r.state);
		} catch (e) {
			this.#error = failure(e);
		} finally {
			this.#opening = false;
			this.#changed();
		}
	}

	#lifecycle(e: LifecycleEvent): void {
		this.#seen.set(e.workspace, e.state);
		if (this.#info !== null && e.workspace === this.#info.id) {
			if (e.state === 'closing' || e.state === 'closed') {
				this.#closed = true;
			} else if (e.state === 'ready' || e.state === 'degraded') {
				this.#closed = false;
				this.#info = { ...this.#info, state: e.state };
			}
		}
		this.#changed();
	}

	/** Events may have been missed: ask the daemon whether the open workspace is still open. */
	async #resync(): Promise<void> {
		const info = this.#info;
		if (info === null) return;
		try {
			const r = await this.#client.call('workspace.describe', {}, { workspace: this.#root.trim() });
			if (this.#info === info && r.id === info.id) {
				this.#info = { ...info, state: r.state, trusted: r.trusted };
				this.#closed = false;
			}
		} catch (e) {
			// Only the daemon's answer says the workspace is gone; a lost connection says nothing.
			if (this.#info === info && e instanceof CapabilityError) {
				this.#closed = true;
				this.#error = failure(e);
			}
		}
		this.#changed();
	}

	#changed(): void {
		this.#view = null;
		for (const l of [...this.#listeners]) l();
	}
}
