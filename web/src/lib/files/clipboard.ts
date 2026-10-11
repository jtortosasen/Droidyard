// The tree's clipboard (plan m2g step 6; D52): in memory, one per project view, never the
// system clipboard. It turns a paste into the items of a call; the call is ops.ts's.
import { nameOf, parentOf } from './ops';
import { topmost } from './selection';

export interface Item {
	from: string;
	to: string;
}

export interface PastePlan {
	kind: 'move' | 'copy';
	/** Items to send as they are (a taken name raises the collision dialog). */
	items: Item[];
	/** Copies into their own folder: sent with `keepBoth`, so each gets a free name. */
	duplicates: Item[];
}

function join(dir: string, name: string): string {
	return dir === '' ? name : `${dir}/${name}`;
}

export class Clipboard {
	#mode: 'cut' | 'copy' | null = null;
	#workspace: string | null = null;
	#paths: string[] = [];

	get mode(): 'cut' | 'copy' | null {
		return this.#mode;
	}

	get paths(): readonly string[] {
		return this.#paths;
	}

	/** Cut or copy the top-most of `paths` for the project `workspaceId`. */
	put(mode: 'cut' | 'copy', workspaceId: string | null, paths: readonly string[]): void {
		this.#paths = topmost(paths);
		this.#mode = this.#paths.length === 0 ? null : mode;
		this.#workspace = this.#paths.length === 0 ? null : workspaceId;
	}

	/** Cut rows are drawn dimmed. */
	isCut(path: string): boolean {
		return this.#mode === 'cut' && this.#paths.includes(path);
	}

	/** Paste is disabled for another project: `file.*` paths belong to one root. */
	canPaste(workspaceId: string | null): boolean {
		return this.#mode !== null && this.#workspace === workspaceId;
	}

	/**
	 * The folder a paste goes to for the selected row: a folder itself, a file's parent.
	 * `dir` says whether the row is a folder.
	 */
	static targetFor(path: string | null, dir: boolean): string {
		if (path === null) return '';
		return dir ? path : parentOf(path);
	}

	/**
	 * Pastes into the folder `dir` ('' is the root): returns what to call, or null when
	 * nothing may be pasted. A cut empties the clipboard; a cut pasted into its own folder
	 * makes no item (no call); a copy into its own folder is a duplicate.
	 */
	paste(workspaceId: string | null, dir: string): PastePlan | null {
		if (!this.canPaste(workspaceId) || this.#mode === null) return null;
		const mode = this.#mode;
		const items: Item[] = [];
		const duplicates: Item[] = [];
		for (const from of this.#paths) {
			const to = join(dir, nameOf(from));
			if (mode === 'cut') {
				if (to !== from) items.push({ from, to });
			} else if (parentOf(from) === dir) {
				duplicates.push({ from, to: from });
			} else {
				items.push({ from, to });
			}
		}
		if (mode === 'cut') {
			this.#mode = null;
			this.#paths = [];
			this.#workspace = null;
		}
		return { kind: mode === 'cut' ? 'move' : 'copy', items, duplicates };
	}
}
