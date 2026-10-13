// The History tab's Checkout and Create Branch (plan m2n P3; ADR-0071 §1, §5, §6): one model per
// project tab, shared by the branch menu, the toolbar's Create Branch and the dialog.
//   - `checkout` and `createBranch` each make one call (`git.checkout`, `git.createBranch`), after
//     the dirty-buffer gate: with an unsaved editor buffer a checkout, and a new branch that is
//     checked out, call nothing and keep the files to name (`blocked`); a new branch that is not
//     checked out is never blocked (branchops.ts);
//   - the call's answer is an outcome per root; the UI sends one root, so the first outcome
//     decides: `done`, or `failed` with git's own words (cause) and a next step, as text;
//   - when the call has ended, however it ended, `onDone` reloads the refs: a refused or ended
//     checkout may still have changed what the list shows, and the list is cheap to read again.
//     That follows the UI's own write and not "another git", so it adds no signal (ADR-0070 §3);
//   - one write at a time here too (the daemon would queue it behind the first): a call asked for
//     while one is in flight is ignored.
// A clean buffer whose file the checkout rewrote reloads through the file watch and the viewer's
// external-change path (plan m2i D8); nothing here touches the editor.
import type { Params, Result } from '../api/capabilities.gen';
import { CapabilityError, type CallOptions } from '../api/client';
import { failure } from '../build/failure';
import { blockText, checkoutParams, createParams, dirtyBlock, type BranchTarget, type DirtySource } from './branchops';

type Calls = 'git.checkout' | 'git.createBranch';

export interface BranchClient {
	call<N extends Calls>(name: N, params: Params<N>, opts?: CallOptions): Promise<Result<N>>;
}

export interface BranchWorkspace {
	readonly root: string;
}

/** What the daemon said of a failed call: git's own words are the cause. */
export interface BranchFailure {
	code?: string;
	message: string;
	/** git's stderr from its first fatal or error line (ADR-0071 §5); text of the repository. */
	cause?: string;
	nextStep?: string;
}

export interface BranchView {
	/** A call is in flight. */
	busy: boolean;
	/** The last call's failure. */
	failure: BranchFailure | null;
	/** The unsaved files that stopped the last call before it was made; empty when none did. */
	blocked: readonly string[];
	/** The message of the block, as text. */
	blockedText: string;
}

function describe(e: unknown): BranchFailure {
	const f = failure(e);
	return { ...f, ...(e instanceof CapabilityError && e.causeText ? { cause: e.causeText } : {}) };
}

export class BranchModel {
	#client: BranchClient;
	#ws: BranchWorkspace;
	#dirty: DirtySource;
	#onDone: () => void;
	#listeners = new Set<() => void>();
	#view: BranchView | null = null;
	#busy = false;
	#failure: BranchFailure | null = null;
	#blocked: readonly string[] = [];
	#disposed = false;

	constructor(client: BranchClient, ws: BranchWorkspace, dirty: DirtySource, onDone: () => void) {
		this.#client = client;
		this.#ws = ws;
		this.#dirty = dirty;
		this.#onDone = onDone;
	}

	get view(): BranchView {
		this.#view ??= { busy: this.#busy, failure: this.#failure, blocked: this.#blocked, blockedText: this.#blocked.length === 0 ? '' : blockText(this.#blocked) };
		return this.#view;
	}

	subscribe(listener: () => void): () => void {
		this.#listeners.add(listener);
		return () => {
			this.#listeners.delete(listener);
		};
	}

	/** The unsaved files a call would meet, so the dialog can say so before it is submitted. */
	dirtyFor(checkout: boolean): string[] {
		return dirtyBlock(this.#dirty, checkout);
	}

	/** Checks `target` out, unless an unsaved buffer stops it. Resolves to whether the branch is checked out. */
	checkout(target: BranchTarget): Promise<boolean> {
		return this.#run(true, 'git.checkout', checkoutParams(target));
	}

	/** Creates `name` at `from`, and checks it out when asked. Resolves to whether it was done. */
	createBranch(name: string, checkout: boolean, from: string): Promise<boolean> {
		return this.#run(checkout, 'git.createBranch', createParams(name, checkout, from));
	}

	/** Forgets a failure or a block. */
	dismiss(): void {
		if (this.#failure === null && this.#blocked.length === 0) return;
		this.#failure = null;
		this.#blocked = [];
		this.#changed();
	}

	dispose(): void {
		this.#disposed = true;
		this.#listeners.clear();
	}

	async #run<N extends Calls>(rewrites: boolean, name: N, params: Params<N>): Promise<boolean> {
		if (this.#disposed || this.#busy) return false;
		const blocked = dirtyBlock(this.#dirty, rewrites);
		if (blocked.length > 0) {
			this.#failure = null;
			this.#blocked = blocked;
			this.#changed();
			return false;
		}
		this.#blocked = [];
		this.#failure = null;
		this.#busy = true;
		this.#changed();
		let failed: BranchFailure | null = null;
		try {
			const res = await this.#client.call(name, params, { workspace: this.#ws.root.trim() });
			failed = outcomeFailure(res.outcomes);
		} catch (e) {
			failed = describe(e);
		}
		this.#busy = false;
		if (this.#disposed) return false;
		this.#failure = failed;
		this.#changed();
		this.#onDone();
		return failed === null;
	}

	#changed(): void {
		this.#view = null;
		for (const l of [...this.#listeners]) l();
	}
}

/** The first root's failure, or null when it was done; an answer with no outcome is a failure too. */
function outcomeFailure(outcomes: Result<'git.checkout'>['outcomes']): BranchFailure | null {
	const first = outcomes[0];
	if (first === undefined) return { message: 'the daemon answered with no outcome' };
	if (first.status === 'done') return null;
	const e = first.error;
	if (e === undefined) return { message: first.status === 'skipped' ? 'the call was not tried' : 'the call failed' };
	return { code: e.code, message: e.message, ...(e.cause ? { cause: e.cause } : {}), ...(e.nextStep ? { nextStep: e.nextStep } : {}) };
}
