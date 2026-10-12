// The tree's file operations over the client (plan m2g step 3; D58). Names are checked
// here, before any call; a failed call or item comes back as text for the inline field.
import type { Params, Result } from '../api/capabilities.gen';
import type { CallOptions } from '../api/client';
import { failure, failureText } from '../build/failure';
import type { Item, PastePlan } from './clipboard';
import { UndoStack, type UndoEntry } from './undo';

export interface OpsClient {
	call(name: 'file.create', params: Params<'file.create'>, opts?: CallOptions): Promise<Result<'file.create'>>;
	call(name: 'file.move', params: Params<'file.move'>, opts?: CallOptions): Promise<Result<'file.move'>>;
	call(name: 'file.copy', params: Params<'file.copy'>, opts?: CallOptions): Promise<Result<'file.copy'>>;
	call(name: 'file.delete', params: Params<'file.delete'>, opts?: CallOptions): Promise<Result<'file.delete'>>;
	call(name: 'file.restore', params: Params<'file.restore'>, opts?: CallOptions): Promise<Result<'file.restore'>>;
}

/** What the view does with a done move (D57): the open tabs follow. */
export interface OpsHooks {
	moved?(from: string, to: string): void;
	/**
	 * Whether `path`, or a file under it, is an open tab with unsaved changes (m2i D7). A move,
	 * copy or paste whose target is one is refused before any call: the target's file is gone
	 * (nothing replaces), and landing a file there would meet the user's unsaved text.
	 */
	unsaved?(path: string): boolean;
	/**
	 * Whether an open file directly in the folder `dir` ('' is the root) has been deleted on
	 * disk but has unsaved changes. The daemon's free name for a Keep both or a duplicate can
	 * be that file's path, since the file is gone (m2i D7).
	 */
	goneUnsaved?(dir: string): boolean;
}

/** Items that came back `FILE_EXISTS`, waiting for the dialog (D54): Keep both or Skip. */
export interface Collision {
	op: 'move' | 'copy';
	items: Item[];
	/** The names in the way, as text for the dialog. */
	names: string[];
}

/** The line under the tree (D56). */
export interface Notice {
	text: string;
	/** Whether the Undo button shows: the notice is of an operation that can be undone. */
	undoable: boolean;
}

export interface OpsWorkspace {
	readonly root: string;
}

export type Outcome = { ok: true; path: string } | { ok: false; message: string };

/** Why a name cannot be used (D58), or null. One path segment: no `/`, no NUL, not empty, `.` or `..`. */
export function refuseName(name: string): string | null {
	if (name === '') return 'A name is required.';
	if (name === '.' || name === '..') return `"${name}" is not a name.`;
	if (name.includes('/')) return 'A name cannot contain "/".';
	if (name.includes('\0')) return 'A name cannot contain a NUL character.';
	return null;
}

function join(dir: string, name: string): string {
	return dir === '' ? name : `${dir}/${name}`;
}

export function parentOf(path: string): string {
	const i = path.lastIndexOf('/');
	return i < 0 ? '' : path.slice(0, i);
}

export function nameOf(path: string): string {
	return path.slice(path.lastIndexOf('/') + 1);
}

/** What a failed item or call says, as text; an existing name reads as "x already exists". */
function describe(code: string | undefined, message: string, name: string, nextStep?: string): string {
	if (code === 'FILE_EXISTS') return `${name} already exists`;
	return failureText({ code, message, nextStep });
}

interface Failed {
	label: string;
	code: string | undefined;
	message: string;
}

/** "Deleted 3 items · 1 failed: x, y" (D56); at most three failures are spelled out. */
function summary(verb: string, done: number, failed: readonly Failed[]): string {
	let text = `${verb} ${done} item${done === 1 ? '' : 's'}`;
	if (failed.length > 0) {
		const shown = failed.slice(0, 3).map((f) => describe(f.code, f.message, f.label));
		text += ` \u00b7 ${failed.length} failed: ${shown.join('; ')}${failed.length > 3 ? '; \u2026' : ''}`;
	}
	return text;
}

export class FileOps {
	#client: OpsClient;
	#ws: OpsWorkspace;
	#hooks: OpsHooks;
	#undo = new UndoStack();
	#listeners = new Set<() => void>();
	#pending: Collision | null = null;
	#notice: Notice | null = null;

	constructor(client: OpsClient, ws: OpsWorkspace, hooks: OpsHooks = {}) {
		this.#client = client;
		this.#ws = ws;
		this.#hooks = hooks;
	}

	/** The collision waiting for an answer, or null. */
	get pending(): Collision | null {
		return this.#pending;
	}

	/** The last operation's line, until the next one. */
	get notice(): Notice | null {
		return this.#notice;
	}

	get canUndo(): boolean {
		return this.#undo.size > 0;
	}

	subscribe(listener: () => void): () => void {
		this.#listeners.add(listener);
		return () => {
			this.#listeners.delete(listener);
		};
	}

	/** A project switch or a closed view drops the stack and the notice (D55). */
	reset(): void {
		this.#undo.clear();
		this.#pending = null;
		this.#notice = null;
		this.#changed();
	}

	#changed(): void {
		for (const l of [...this.#listeners]) l();
	}

	/** D7: the text refusing an operation that would land on an open buffer with unsaved changes, or null. */
	#unsaved(targets: readonly string[]): string | null {
		const hit = targets.find((t) => this.#hooks.unsaved?.(t) === true);
		return hit === undefined ? null : `${hit} is open with unsaved changes; save or close it first.`;
	}

	/** D7: the text refusing a Keep both or duplicate into a folder holding a deleted file's unsaved tab, or null. */
	#goneUnsaved(dirs: readonly string[]): string | null {
		const hit = dirs.find((d) => this.#hooks.goneUnsaved?.(d) === true);
		return hit === undefined
			? null
			: `${hit === '' ? 'the project root' : hit} has an open file that was deleted and has unsaved changes, and the new name could be its path; save or close it first.`;
	}

	#opts(): CallOptions {
		return { workspace: this.#ws.root.trim() };
	}

	#say(text: string, undoable: boolean): void {
		this.#notice = { text, undoable };
		this.#changed();
	}

	/** Creates `name` in the folder `dir` ('' is the root). */
	async create(dir: string, name: string, type: 'file' | 'dir'): Promise<Outcome> {
		const why = refuseName(name);
		if (why !== null) return { ok: false, message: why };
		const path = join(dir, name);
		try {
			const r = await this.#client.call('file.create', { path, type }, this.#opts());
			this.#undo.push({ kind: 'made', paths: [r.path] });
			// The notice always names the top of the Undo stack, so its button undoes what it says.
			this.#say(`Created ${nameOf(r.path)}`, true);
			return { ok: true, path: r.path };
		} catch (e) {
			const f = failure(e);
			return { ok: false, message: describe(f.code, f.message, name, f.nextStep) };
		}
	}

	/** Renames the entry at `path` to `name` in the same folder: a one-item `file.move`. */
	async rename(path: string, name: string): Promise<Outcome> {
		const why = refuseName(name);
		if (why !== null) return { ok: false, message: why };
		const to = join(parentOf(path), name);
		if (to === path) return { ok: true, path };
		const held = this.#unsaved([to]);
		if (held !== null) return { ok: false, message: held };
		try {
			const r = await this.#client.call('file.move', { items: [{ from: path, to }] }, this.#opts());
			const item = r.items[0];
			if (item === undefined) return { ok: false, message: 'The daemon returned no result for the rename.' };
			if (item.code !== undefined) return { ok: false, message: describe(item.code, item.message ?? item.code, name) };
			this.#movedDone([item]);
			this.#say(`Renamed ${nameOf(path)} to ${nameOf(item.to)}`, true);
			return { ok: true, path: item.to };
		} catch (e) {
			const f = failure(e);
			return { ok: false, message: describe(f.code, f.message, name, f.nextStep) };
		}
	}

	/** Moves `items`; a taken target raises the collision (D54). */
	move(items: readonly Item[]): Promise<void> {
		return this.#batch('move', items, false);
	}

	/** Copies `items`; a taken target raises the collision (D54). */
	copy(items: readonly Item[]): Promise<void> {
		return this.#batch('copy', items, false);
	}

	/** Duplicates `paths`: a copy onto each path itself, with a free name (D52). */
	duplicate(paths: readonly string[]): Promise<void> {
		return this.#batch(
			'copy',
			paths.map((p) => ({ from: p, to: p })),
			true,
		);
	}

	/** A paste (clipboard.ts): the items as they are, then the duplicates with `keepBoth`. */
	async paste(plan: PastePlan): Promise<void> {
		await this.#batch(plan.kind, plan.items, false);
		if (plan.duplicates.length > 0) await this.#batch('copy', plan.duplicates, true);
	}

	async #batch(op: 'move' | 'copy', items: readonly Item[], keepBoth: boolean, retried = false): Promise<void> {
		if (items.length === 0) return;
		// A named target is checked as it stands. A duplicate or a Keep both lands on a free name the
		// daemon picks in the target's folder, and the free name can be the path of an open tab
		// whose file is gone and whose text is unsaved: those folders are checked instead.
		const held = keepBoth ? this.#goneUnsaved(items.map((i) => parentOf(i.to))) : this.#unsaved(items.map((i) => i.to));
		if (held !== null) {
			this.#say(`${op === 'move' ? 'Move' : 'Copy'} refused: ${held}`, false);
			return;
		}
		const params = { items: items as [Item, ...Item[]], ...(keepBoth ? { onCollision: 'keepBoth' as const } : {}) };
		let res: Result<'file.move'> | Result<'file.copy'>;
		try {
			res = op === 'move' ? await this.#client.call('file.move', params, this.#opts()) : await this.#client.call('file.copy', params, this.#opts());
		} catch (e) {
			this.#say(`${op === 'move' ? 'Move' : 'Copy'} failed: ${failureText(failure(e))}`, false);
			return;
		}
		const done = res.items.filter((i) => i.code === undefined);
		// FILE_EXISTS asks the dialog, once; after Keep both it is just a failure.
		const taken = retried || keepBoth ? [] : res.items.filter((i) => i.code === 'FILE_EXISTS');
		const failed: Failed[] = res.items
			.filter((i) => i.code !== undefined && !taken.includes(i))
			.map((i) => ({ label: nameOf(i.to), code: i.code, message: i.message ?? i.code! }));
		if (op === 'move') {
			this.#movedDone(done);
		} else {
			this.#undo.push({ kind: 'made', paths: done.map((i) => i.to) });
		}
		if (taken.length > 0) {
			this.#pending = { op, items: taken.map((i) => ({ from: i.from, to: i.to })), names: taken.map((i) => nameOf(i.to)) };
		}
		if (done.length > 0 || failed.length > 0 || taken.length === 0) {
			this.#say(summary(op === 'move' ? 'Moved' : 'Copied', done.length, failed), done.length > 0);
		} else {
			this.#changed();
		}
	}

	/** The dialog's answer (D54): Keep both repeats the call for the taken items with `keepBoth`; Skip drops them. */
	async resolveCollision(answer: 'keepBoth' | 'skip'): Promise<void> {
		const p = this.#pending;
		if (p === null) return;
		this.#pending = null;
		if (answer === 'skip') {
			this.#changed();
			return;
		}
		await this.#batch(p.op, p.items, true, true);
	}

	/** Deletes `paths` to the trash, no confirmation (D56). */
	async delete(paths: readonly string[]): Promise<void> {
		if (paths.length === 0) return;
		let res: Result<'file.delete'>;
		try {
			res = await this.#client.call('file.delete', { paths: paths as [string, ...string[]] }, this.#opts());
		} catch (e) {
			this.#say(`Delete failed: ${failureText(failure(e))}`, false);
			return;
		}
		const ids = res.items.flatMap((i) => (i.code === undefined && i.trashId !== undefined ? [i.trashId] : []));
		const failed = res.items.filter((i) => i.code !== undefined).map((i): Failed => ({ label: nameOf(i.path), code: i.code, message: i.message ?? i.code! }));
		this.#undo.push({ kind: 'delete', trashIds: ids });
		this.#say(summary('Deleted', ids.length, failed), ids.length > 0);
	}

	/** Restores trash entries by id (an undone delete, or a caller holding ids). */
	async restore(trashIds: readonly string[]): Promise<void> {
		if (trashIds.length === 0) return;
		await this.#inverse({ kind: 'delete', trashIds: [...trashIds] });
	}

	/** Undoes the last operation (⌘Z or the notice's button): its inverse, nothing forced, no redo (D55). */
	async undo(): Promise<void> {
		const entry = this.#undo.pop();
		if (entry === undefined) return;
		await this.#inverse(entry);
	}

	async #inverse(entry: UndoEntry): Promise<void> {
		try {
			if (entry.kind === 'delete') {
				const r = await this.#client.call('file.restore', { trashIds: entry.trashIds as [string, ...string[]] }, this.#opts());
				const failed = r.items.filter((i) => i.code !== undefined).map((i): Failed => ({ label: i.trashId, code: i.code, message: i.message ?? i.code! }));
				this.#say(summary('Restored', r.items.length - failed.length, failed), false);
			} else if (entry.kind === 'move') {
				// Back in the opposite order, and no collision handling: an occupied origin is reported.
				const back = entry.items.map((i) => ({ from: i.to, to: i.from })).reverse();
				const held = this.#unsaved(back.map((i) => i.to));
				if (held !== null) {
					this.#undo.push(entry); // nothing was undone: it stays on the stack for after the save
					this.#say(`Undo refused: ${held}`, true);
					return;
				}
				const r = await this.#client.call('file.move', { items: back as [Item, ...Item[]] }, this.#opts());
				const done = r.items.filter((i) => i.code === undefined);
				this.#movedDone(done, false);
				const failed = r.items.filter((i) => i.code !== undefined).map((i): Failed => ({ label: nameOf(i.to), code: i.code, message: i.message ?? i.code! }));
				this.#say(summary('Moved back', done.length, failed), false);
			} else {
				const r = await this.#client.call('file.delete', { paths: entry.paths as [string, ...string[]] }, this.#opts());
				const failed = r.items.filter((i) => i.code !== undefined).map((i): Failed => ({ label: nameOf(i.path), code: i.code, message: i.message ?? i.code! }));
				this.#say(summary('Removed', r.done, failed), false);
			}
		} catch (e) {
			this.#say(`Undo failed: ${failureText(failure(e))}`, false);
		}
	}

	/** Done moves: the tabs follow (D57), and the operation can be undone. */
	#movedDone(done: readonly Item[], record = true): void {
		for (const i of done) this.#hooks.moved?.(i.from, i.to);
		if (record) this.#undo.push({ kind: 'move', items: done.map((i) => ({ from: i.from, to: i.to })) });
	}
}
