// The tree's multi-selection (plan m2g step 6; D51). Pure: it knows paths and the visible
// rows' order, nothing of the DOM. `anchor` is where a range starts, `focus` the row the
// keyboard is on.

export class Selection {
	#set = new Set<string>();
	#anchor: string | null = null;
	#focus: string | null = null;

	get paths(): ReadonlySet<string> {
		return this.#set;
	}

	get anchor(): string | null {
		return this.#anchor;
	}

	get focus(): string | null {
		return this.#focus;
	}

	has(path: string): boolean {
		return this.#set.has(path);
	}

	/** A plain click: this row alone, and the new anchor. */
	click(path: string | null): void {
		this.#set = path === null ? new Set() : new Set([path]);
		this.#anchor = path;
		this.#focus = path;
	}

	/** ⌘-click: the row joins or leaves; it becomes the anchor. */
	toggle(path: string): void {
		if (!this.#set.delete(path)) this.#set.add(path);
		this.#anchor = path;
		this.#focus = path;
	}

	/** ⇧-click: the rows between the anchor and `path`, in the order of `rows` (the visible paths). */
	range(path: string, rows: readonly string[]): void {
		const a = this.#anchor === null ? -1 : rows.indexOf(this.#anchor);
		const b = rows.indexOf(path);
		if (a < 0 || b < 0) {
			this.click(path);
			return;
		}
		this.#set = new Set(rows.slice(Math.min(a, b), Math.max(a, b) + 1));
		this.#focus = path;
	}

	/** ⇧↑ / ⇧↓: the range grows or shrinks from the anchor by one row. Returns the new focus. */
	extend(delta: -1 | 1, rows: readonly string[]): string | null {
		if (rows.length === 0) return null;
		const from = this.#focus === null ? -1 : rows.indexOf(this.#focus);
		const to = Math.min(rows.length - 1, Math.max(0, from + delta));
		const target = rows[to]!;
		if (this.#anchor === null || !rows.includes(this.#anchor)) this.#anchor = this.#focus !== null && from >= 0 ? this.#focus : target;
		this.range(target, rows);
		return target;
	}

	/** Drops what is no longer there (`keep` says whether a path still exists). */
	prune(keep: (path: string) => boolean): void {
		for (const p of [...this.#set]) if (!keep(p)) this.#set.delete(p);
		if (this.#anchor !== null && !keep(this.#anchor)) this.#anchor = null;
		if (this.#focus !== null && !keep(this.#focus)) this.#focus = null;
	}

	/** What an operation acts on: the selected paths without the descendants of a selected folder. */
	topmost(): string[] {
		return topmost([...this.#set]);
	}
}

/** `paths` without any path under another one in the list. */
export function topmost(paths: readonly string[]): string[] {
	const set = new Set(paths);
	return paths.filter((p) => {
		for (let i = p.indexOf('/'); i >= 0; i = p.indexOf('/', i + 1)) if (set.has(p.slice(0, i))) return false;
		return true;
	});
}
