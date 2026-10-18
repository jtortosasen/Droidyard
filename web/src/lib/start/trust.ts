// Trust from the UI (ADR-0045 §5, ADR-0015 §5): an untrusted project says what trusting
// means and offers Trust. The click is the only way the UI grants trust: opening a project
// never does. A grant reads the policy, then writes the trusted roots plus this one with
// the revision it read; Merge Patch replaces an array whole, so the roots already trusted
// must be sent back with the new one. A concurrent edit of the policy (POLICY_REVISION_
// CONFLICT) is not retried behind the user's back: the policy is read again and, if the
// project is still not trusted, the user is asked again.
import type { Params, Result } from '../api/capabilities.gen';
import { CapabilityError, type CallOptions } from '../api/client';
import { failure, type Failure } from '../build/failure';

type Calls = 'policy.get' | 'policy.update';

export interface TrustClient {
	call<N extends Calls>(name: N, params: Params<N>, opts?: CallOptions): Promise<Result<N>>;
}

/**
 * The workspace being trusted: its root, the main tree when the root is a linked worktree
 * (trust is the project's, ADR-0081 §5), and a re-read of its trusted mark.
 */
export interface TrustWorkspace {
	readonly root: string;
	readonly mainRoot?: string;
	refresh(): Promise<void>;
}

export interface TrustView {
	busy: boolean;
	/** The policy changed while the user decided: it was read again, Trust asks again. */
	conflict: boolean;
	error: Failure | null;
}

export class TrustModel {
	#client: TrustClient;
	#ws: TrustWorkspace;
	#busy = false;
	#conflict = false;
	#error: Failure | null = null;
	#view: TrustView | null = null;
	#listeners = new Set<() => void>();

	constructor(client: TrustClient, ws: TrustWorkspace) {
		this.#client = client;
		this.#ws = ws;
	}

	/** The main tree a grant trusts when the workspace is a linked worktree; '' otherwise. */
	get mainRoot(): string {
		return this.#ws.mainRoot ?? '';
	}

	get view(): TrustView {
		this.#view ??= { busy: this.#busy, conflict: this.#conflict, error: this.#error };
		return this.#view;
	}

	subscribe(listener: () => void): () => void {
		this.#listeners.add(listener);
		return () => {
			this.#listeners.delete(listener);
		};
	}

	/** The user's click on Trust. */
	async grant(): Promise<void> {
		// On a worktree the grant is for the project: its main tree (ADR-0081 §5).
		const root = this.#ws.mainRoot || this.#ws.root;
		if (root === '' || this.#busy) return;
		this.#busy = true;
		this.#conflict = false;
		this.#error = null;
		this.#changed();
		try {
			const p = await this.#client.call('policy.get', {});
			if (!p.trust.roots.includes(root)) {
				await this.#client.call('policy.update', {
					trust: { roots: [...p.trust.roots, root] },
					ifRevision: p.revision,
				});
			}
			await this.#ws.refresh();
		} catch (e) {
			if (e instanceof CapabilityError && e.code === 'POLICY_REVISION_CONFLICT') {
				await this.#afterConflict(root);
			} else {
				this.#error = failure(e);
			}
		} finally {
			this.#busy = false;
			this.#changed();
		}
	}

	/** Read the policy again: another client may have trusted this root, or changed the list. */
	async #afterConflict(root: string): Promise<void> {
		try {
			const p = await this.#client.call('policy.get', {});
			if (p.trust.roots.includes(root)) await this.#ws.refresh();
			else this.#conflict = true;
		} catch (e) {
			this.#error = failure(e);
		}
	}

	#changed(): void {
		this.#view = null;
		for (const l of [...this.#listeners]) l();
	}
}
