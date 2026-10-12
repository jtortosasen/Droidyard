// The second-level tabs of the Editor (plan m2d step 6; D25), one set per project.
//
//   - A single click on a tree file opens a PREVIEW tab: shown in italics, and the next
//     preview replaces it (VS Code's preview tab). Opening the file that is already the
//     preview again, or double-clicking its tab, makes it permanent; so does a second
//     open of any open tab, which only focuses it. A tab is never silently duplicated.
//   - A tab with unsaved changes carries a mark (`setDirty`); the first edit makes a preview
//     tab permanent, and a mark follows its tab through a move (m2i D7).
//   - The tabs are remembered per browser and project in localStorage (D25), every access
//     in try/catch and every value checked on the way in. On restore a path that no longer
//     exists is dropped; nothing is written until the restore has run, so a slow start
//     cannot overwrite what the last session left.
import type { KeyValueStorage } from '../ui/layout';

export const STORAGE_PREFIX = 'droidyard.ui.tabs.';
/** Bounds on what a stored entry may hold. */
export const MAX_TABS = 50;
export const MAX_PATH = 1024;

export interface Tab {
	path: string;
	/** A preview tab: italics, replaced by the next preview. */
	preview: boolean;
	/** The file has unsaved changes (m2i D7); present only when true, and never stored. */
	dirty?: true;
}

export interface TabsView {
	tabs: readonly Tab[];
	active: string | null;
}

export class TabsModel {
	#storage: KeyValueStorage | null;
	#key: string;
	#tabs: Tab[] = [];
	#dirty = new Set<string>();
	#active: string | null = null;
	#view: TabsView | null = null;
	#listeners = new Set<() => void>();
	#restored = false;
	#stored: { tabs: Tab[]; active: string | null };

	constructor(storage: KeyValueStorage | null, project: string) {
		this.#storage = storage;
		this.#key = STORAGE_PREFIX + project;
		this.#stored = this.#load();
	}

	get view(): TabsView {
		this.#view ??= { tabs: this.#tabs.map((t) => (this.#dirty.has(t.path) ? { ...t, dirty: true as const } : t)), active: this.#active };
		return this.#view;
	}

	subscribe(listener: () => void): () => void {
		this.#listeners.add(listener);
		return () => {
			this.#listeners.delete(listener);
		};
	}

	dispose(): void {
		this.#listeners.clear();
	}

	/**
	 * Opens the remembered tabs whose file still exists. `exists` answers per path; a path it
	 * rejects for is kept (a probe that failed says nothing about the file).
	 */
	async restore(exists: (path: string) => Promise<boolean>): Promise<void> {
		const wanted = this.#stored;
		const kept: Tab[] = [];
		for (const t of wanted.tabs) {
			let ok = true;
			try {
				ok = await exists(t.path);
			} catch {
				ok = true;
			}
			if (ok) kept.push(t);
		}
		this.#restored = true;
		// Whatever the user opened while this ran stays, after the restored ones.
		const now = this.#tabs.filter((t) => !kept.some((k) => k.path === t.path));
		this.#tabs = [...kept, ...now].slice(0, MAX_TABS);
		if (this.#active === null || !this.#tabs.some((t) => t.path === this.#active)) {
			const a = wanted.active;
			this.#active = a !== null && this.#tabs.some((t) => t.path === a) ? a : (this.#tabs[0]?.path ?? null);
		}
		this.#changed();
	}

	/** A file was chosen in the tree. */
	open(path: string): void {
		const at = this.#tabs.findIndex((t) => t.path === path);
		if (at >= 0) {
			// Opened again without an edit in between: it is kept.
			if (this.#tabs[at]!.preview) this.#tabs[at] = { path, preview: false };
			this.#active = path;
		} else {
			const p = this.#tabs.findIndex((t) => t.preview);
			if (p >= 0) this.#tabs[p] = { path, preview: true };
			else this.#tabs.push({ path, preview: true });
			this.#active = path;
		}
		this.#changed();
	}

	/** Makes a tab permanent (a double-click on it). */
	keep(path: string): void {
		const at = this.#tabs.findIndex((t) => t.path === path);
		if (at < 0 || !this.#tabs[at]!.preview) return;
		this.#tabs[at] = { path, preview: false };
		this.#changed();
	}

	/** A file's unsaved-changes mark; the first edit also makes a preview tab permanent. */
	setDirty(path: string, dirty: boolean): void {
		const at = this.#tabs.findIndex((t) => t.path === path);
		if (!dirty) {
			if (this.#dirty.delete(path)) this.#changed();
			return;
		}
		if (at < 0) return;
		const had = this.#dirty.has(path);
		this.#dirty.add(path);
		if (this.#tabs[at]!.preview) this.#tabs[at] = { path, preview: false };
		else if (had) return;
		this.#changed();
	}

	/** Whether the tab at `path` has the unsaved-changes mark. */
	isDirty(path: string): boolean {
		return this.#dirty.has(path);
	}

	activate(path: string): void {
		if (this.#active === path || !this.#tabs.some((t) => t.path === path)) return;
		this.#active = path;
		this.#changed();
	}

	/**
	 * A file or folder was moved or renamed (D57): its tab, and the tabs under a moved folder
	 * (by path prefix, never a sibling that merely shares the prefix), follow it.
	 */
	moved(from: string, to: string): void {
		const re = (p: string): string => (p === from ? to : p.startsWith(`${from}/`) ? to + p.slice(from.length) : p);
		const movers = this.#tabs.filter((t) => re(t.path) !== t.path);
		if (movers.length === 0) return;
		const landing = new Set(movers.map((t) => re(t.path)));
		const staying = new Set(this.#tabs.filter((t) => re(t.path) === t.path).map((t) => t.path));
		const next: Tab[] = [];
		const dirty = new Set<string>();
		const at = new Map<string, string>(); // where each kept tab ended up
		for (const t of this.#tabs) {
			const path = re(t.path);
			if (path === t.path) {
				// A tab at a target is replaced by the moved one (the file there is gone, since a
				// move never replaces), unless it has unsaved changes, which are never dropped.
				if (landing.has(path) && !this.#dirty.has(path)) continue;
				next.push(t);
				at.set(t.path, path);
				if (this.#dirty.has(path)) dirty.add(path);
			} else if (staying.has(path) && this.#dirty.has(path)) {
				// The tab at the target has unsaved text and stays. A clean moved tab gives way to it;
				// a moved tab with unsaved text too (FileOps refuses such a move before it is made, so
				// this is the safety net) keeps its old path and its mark rather than lose them.
				if (!this.#dirty.has(t.path)) continue;
				next.push(t);
				at.set(t.path, t.path);
				dirty.add(t.path);
			} else {
				next.push({ path, preview: t.preview });
				at.set(t.path, path);
				if (this.#dirty.has(t.path)) dirty.add(path);
			}
		}
		this.#tabs = next;
		this.#dirty = dirty;
		if (this.#active !== null) {
			const a = at.get(this.#active);
			this.#active = a !== undefined ? a : (next[0]?.path ?? null);
		}
		this.#changed();
	}

	/** Closes a tab; the one to its right (else left) becomes the active one. Returns the new active path. */
	close(path: string): string | null {
		const at = this.#tabs.findIndex((t) => t.path === path);
		if (at < 0) return this.#active;
		this.#tabs.splice(at, 1);
		this.#dirty.delete(path);
		if (this.#active === path) this.#active = (this.#tabs[at] ?? this.#tabs[at - 1])?.path ?? null;
		this.#changed();
		return this.#active;
	}

	#changed(): void {
		this.#tabs = [...this.#tabs];
		this.#view = null;
		if (this.#restored) this.#save();
		for (const l of [...this.#listeners]) l();
	}

	#load(): { tabs: Tab[]; active: string | null } {
		const none = { tabs: [], active: null };
		if (this.#storage === null) return none;
		let parsed: unknown;
		try {
			const raw = this.#storage.getItem(this.#key);
			if (raw === null) return none;
			parsed = JSON.parse(raw);
		} catch {
			return none;
		}
		if (typeof parsed !== 'object' || parsed === null) return none;
		const p = parsed as { tabs?: unknown; active?: unknown };
		const tabs: Tab[] = [];
		if (Array.isArray(p.tabs)) {
			for (const t of p.tabs as unknown[]) {
				if (typeof t !== 'object' || t === null) continue;
				const { path, preview } = t as { path?: unknown; preview?: unknown };
				if (typeof path !== 'string' || path === '' || path.length > MAX_PATH || tabs.some((x) => x.path === path)) continue;
				tabs.push({ path, preview: preview === true && !tabs.some((x) => x.preview) });
				if (tabs.length === MAX_TABS) break;
			}
		}
		const active = typeof p.active === 'string' && tabs.some((t) => t.path === p.active) ? p.active : null;
		return { tabs, active };
	}

	#save(): void {
		if (this.#storage === null) return;
		try {
			this.#storage.setItem(this.#key, JSON.stringify({ tabs: this.#tabs, active: this.#active }));
		} catch {
			// Not remembered; the tabs in memory are still right.
		}
	}
}
