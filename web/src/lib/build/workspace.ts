// The workspace the panels work on (plan m2b D10 and §Changes, m2c step 10). The tab's
// project is the address's `w` (ADR-0045 §2): the model opens it by id, `workspace.open`
// answers with the root, and every other panel addresses its calls by that root and
// matches events on the id. Nothing is kept in storage. When the open workspace is closed
// by another client the panel says so and offers to reopen it; it never reopens by itself
// after that (a closed workspace is a decision, mcp.md).
import type { Params, Result } from '../api/capabilities.gen';
import { CapabilityError, type CallOptions } from '../api/client';
import { failure, type Failure } from './failure';
import type { EventsHub, LifecycleEvent } from './hub';

type Calls = 'workspace.open' | 'workspace.describe';

export interface WorkspaceClient {
	call<N extends Calls>(name: N, params: Params<N>, opts?: CallOptions): Promise<Result<N>>;
}

export interface WorkspaceInfo {
	id: string;
	name: string;
	state: string;
	trusted: boolean;
	/** The project's main tree when the root is a linked git worktree of it (ADR-0082 §6). */
	mainRoot?: string;
	degradedBy?: Failure;
}

/** What the daemon says about the Gradle root (ADR-0075 §5); `describe` is its one source for the UI. */
export type GradleInfo = Result<'workspace.describe'>['gradle'];

export interface WorkspaceView {
	/** The open workspace's root; empty until the first open answered. */
	root: string;
	/** The workspace this panel opened; null before it and while it is not open. */
	info: WorkspaceInfo | null;
	/** The Gradle root and candidates as the daemon last said; null before the first open answered. */
	gradle: GradleInfo | null;
	opening: boolean;
	error: Failure | null;
	/** The open workspace was closed by another client: the panel offers to reopen. */
	closedByOther: boolean;
	/** Workspaces seen on workspace.lifecycle, by id, with their last state (opened by anyone). */
	seen: readonly { id: string; state: string }[];
}

export class WorkspaceModel {
	#client: WorkspaceClient;
	/** The root once known, else the id the tab was opened with: what the open call addresses. */
	#root = '';
	#target = '';
	#info: WorkspaceInfo | null = null;
	#gradle: GradleInfo | null = null;
	#opening = false;
	#error: Failure | null = null;
	#closed = false;
	#seen = new Map<string, string>();
	#listeners = new Set<() => void>();
	#view: WorkspaceView | null = null;
	#offs: (() => void)[] = [];

	constructor(client: WorkspaceClient, hub: Pick<EventsHub, 'onLifecycle' | 'onResync'>) {
		this.#client = client;
		this.#offs.push(hub.onLifecycle((e) => this.#lifecycle(e)));
		this.#offs.push(hub.onResync(() => void this.#resync()));
	}

	get root(): string {
		return this.#root;
	}

	/** The open workspace's trust, as the daemon last described it. */
	get trusted(): boolean {
		return this.#info?.trusted === true;
	}

	/** The main tree when the open workspace is a linked worktree; undefined otherwise. */
	get mainRoot(): string | undefined {
		return this.#info?.mainRoot;
	}

	/** The Gradle root the daemon resolves, relative to the project root; undefined when there is none. */
	get gradleRoot(): string | undefined {
		return this.#gradle?.root;
	}

	/** The id of the open workspace; null when none is (or it was closed by another client). */
	get id(): string | null {
		return this.#info !== null && !this.#closed ? this.#info.id : null;
	}

	get view(): WorkspaceView {
		this.#view ??= {
			root: this.#root,
			info: this.#info,
			gradle: this.#gradle,
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

	/** Opens the tab's project by its workspace id (the address's `w`); false when it did not open. */
	openById(id: string): Promise<boolean> {
		this.#target = id.trim();
		return this.open();
	}

	/** `workspace.open` for the project; also the "reopen" after another client closed it. */
	async open(): Promise<boolean> {
		const target = this.#root !== '' ? this.#root : this.#target;
		if (target === '' || this.#opening) return false;
		this.#opening = true;
		this.#error = null;
		this.#changed();
		let ok = false;
		try {
			const r = await this.#client.call('workspace.open', {}, { workspace: target });
			this.#root = r.root;
			this.#info = {
				id: r.id,
				name: r.name,
				state: r.state,
				trusted: r.trusted,
				mainRoot: r.mainRoot,
				degradedBy: r.degradedBy ? { code: r.degradedBy.code, message: r.degradedBy.message, nextStep: r.degradedBy.nextStep } : undefined,
			};
			this.#gradle = r.gradle;
			this.#closed = false;
			this.#seen.set(r.id, r.state);
			ok = true;
		} catch (e) {
			this.#error = failure(e);
		} finally {
			this.#opening = false;
			this.#changed();
		}
		return ok;
	}

	/** Reads the workspace again: a trust granted from the UI publishes no event. */
	refresh(): Promise<void> {
		return this.#resync();
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
				this.#info = { ...info, state: r.state, trusted: r.trusted, mainRoot: r.mainRoot };
				this.#gradle = r.gradle;
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
