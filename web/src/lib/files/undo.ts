// The undo stack (plan m2g step 6; D55): the project view's last 20 operations, memory
// only. Each entry holds what is needed to reverse what was done, and only what was done.

/** How many operations are kept. */
export const UNDO_LIMIT = 20;

export type UndoEntry =
	/** A delete: `file.restore` of its trash ids. */
	| { kind: 'delete'; trashIds: string[] }
	/** A move or rename: a move back of the done items. */
	| { kind: 'move'; items: { from: string; to: string }[] }
	/** A copy, duplicate or create: a delete (to the trash) of the paths it made. */
	| { kind: 'made'; paths: string[] };

export class UndoStack {
	#entries: UndoEntry[] = [];

	get size(): number {
		return this.#entries.length;
	}

	/** Records an operation; one that did nothing is not recorded. The oldest goes past the limit. */
	push(entry: UndoEntry): void {
		const n = entry.kind === 'delete' ? entry.trashIds.length : entry.kind === 'move' ? entry.items.length : entry.paths.length;
		if (n === 0) return;
		this.#entries.push(entry);
		if (this.#entries.length > UNDO_LIMIT) this.#entries.shift();
	}

	/** Takes the last operation off; there is no redo. */
	pop(): UndoEntry | undefined {
		return this.#entries.pop();
	}

	clear(): void {
		this.#entries = [];
	}
}
