// The question asked before unsaved text could be lost (plan m2i D7): closing a dirty tab,
// leaving the project, unloading the page. One guard per page; the open project attaches
// the set of its models as the source, and whoever is about to lose text asks `confirm`.
// The dialog (Save, Don't save, Cancel) is a view of `pending`.

/** What the guard needs from the open project's models. */
export interface UnsavedSource {
	dirtyPaths(): string[];
	/** Saves these paths in turn; the first failure as text, or null when all are saved. */
	saveAll(paths: readonly string[]): Promise<string | null>;
	/** Drops the models of these paths with their unsaved changes. */
	discard(paths: readonly string[]): void;
}

export interface Pending {
	/** The files with unsaved changes the action would lose. */
	paths: readonly string[];
	/** Why Save did not finish, as text. */
	error: string | null;
	/** A save is under way. */
	busy: boolean;
}

export type Answer = 'save' | 'discard' | 'cancel';

export class UnsavedGuard {
	#source: UnsavedSource | null = null;
	#pending: Pending | null = null;
	#proceed: (() => void) | null = null;
	#listeners = new Set<() => void>();

	get pending(): Pending | null {
		return this.#pending;
	}

	subscribe(listener: () => void): () => void {
		this.#listeners.add(listener);
		return () => {
			this.#listeners.delete(listener);
		};
	}

	/** The open project's models; returns how to detach them (the project went away). */
	attach(source: UnsavedSource): () => void {
		this.#source = source;
		return () => {
			if (this.#source === source) this.#source = null;
		};
	}

	/** Whether any open file has unsaved changes (the page unload asks this synchronously). */
	hasDirty(): boolean {
		return (this.#source?.dirtyPaths().length ?? 0) > 0;
	}

	/**
	 * Runs `proceed` when no file of `paths` (all of them when absent) has unsaved changes;
	 * otherwise asks, and runs it after Save (all saved) or Don't save. Returns whether it ran at once.
	 */
	confirm(proceed: () => void, paths?: readonly string[]): boolean {
		if (this.#pending !== null) return false; // one question at a time
		const source = this.#source;
		const dirty = source === null ? [] : source.dirtyPaths().filter((p) => paths === undefined || paths.includes(p));
		if (dirty.length === 0) {
			proceed();
			return true;
		}
		this.#proceed = proceed;
		this.#pending = { paths: dirty, error: null, busy: false };
		this.#changed();
		return false;
	}

	async answer(a: Answer): Promise<void> {
		const p = this.#pending;
		const proceed = this.#proceed;
		const source = this.#source;
		if (p === null || proceed === null || p.busy) return;
		if (a === 'cancel') {
			this.#close();
			return;
		}
		if (a === 'discard') {
			source?.discard(p.paths);
			this.#close();
			proceed();
			return;
		}
		this.#set({ ...p, busy: true, error: null });
		const error = source === null ? null : await source.saveAll(p.paths);
		// The dialog may have been cancelled or replaced while the save ran.
		if (this.#pending === null || this.#proceed !== proceed) return;
		if (error !== null) {
			this.#set({ paths: p.paths, busy: false, error });
			return;
		}
		this.#close();
		proceed();
	}

	#set(p: Pending): void {
		this.#pending = p;
		this.#changed();
	}

	#close(): void {
		this.#pending = null;
		this.#proceed = null;
		this.#changed();
	}

	#changed(): void {
		for (const l of [...this.#listeners]) l();
	}
}
