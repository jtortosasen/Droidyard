// The command palette's model (plan m2d step 9; D23, D26). Two modes, told by the text:
//   - files (⌘P): the project's files, from the root watch's baseline plus its later
//     events (watch.ts hands both to `FileIndex`; there is no second `file.watch`), found
//     with fuzzysort, the file name weighted over the directory;
//   - commands (⇧⌘P, or a leading `>`): a registry built from the models the page already
//     has. A command that cannot run now is listed disabled with its reason, never hidden.
// Deleting the `>` (Backspace on an empty command query) is typing a query without it, so
// the mode is a function of the text and there is no second state to keep in step.
//
// Strings reach the view as text and the indexes of matched characters (`segments`); no
// markup is made from a file name or a task path.
import fuzzysort, { type Prepared } from 'fuzzysort';
import type { Baseline, Change, WatchListener } from '../files/watch';
import { fileIcon, type IconKind } from '../files/tree';
import type { BuildView } from '../build/build';
import type { TasksView } from '../build/tasks';
import type { EmulatorsView } from '../devices/emulators';
import type { ProjectsView } from '../projects/projects';
import type { Density } from '../ui/layout';

/** D26: the palette shows at most this many results. */
export const PALETTE_LIMIT = 50;
/** The lowest score a match may have; fuzzysort's default of 0.5 drops loose but useful matches. */
const THRESHOLD = 0.3;
/** A match in the directory counts for less than one in the file name (D26: the name is weighted). */
const DIR_WEIGHT = 0.5;

interface FileEntry {
	path: string;
	name: string;
	dir: string;
	/** Prepared once, searched often. */
	nameKey: Prepared;
	dirKey: Prepared | null;
}

/** The project's files: the watch's baseline, then its events. Directories are not listed. */
export class FileIndex implements WatchListener {
	#files = new Map<string, FileEntry>();
	#list: FileEntry[] | null = null;
	#complete = true;

	/** Whether the baseline walk covered the whole project; false means the list is a part of it. */
	get complete(): boolean {
		return this.#complete;
	}

	get size(): number {
		return this.#files.size;
	}

	get entries(): readonly FileEntry[] {
		this.#list ??= [...this.#files.values()];
		return this.#list;
	}

	baseline(entries: Baseline, complete: boolean): void {
		this.#files.clear();
		this.#complete = complete;
		for (const e of entries) if (e.type === 'file') this.#add(e.path);
		this.#list = null;
	}

	changes(list: Change[]): void {
		for (const c of list) {
			if (c.kind === 'created') {
				if ((c.type ?? 'file') === 'file') this.#add(c.path);
			} else if (c.kind === 'deleted') {
				// A removed folder may arrive as itself alone.
				this.#files.delete(c.path);
				const under = `${c.path}/`;
				for (const p of [...this.#files.keys()]) if (p.startsWith(under)) this.#files.delete(p);
			}
		}
		this.#list = null;
	}

	/** The index holds no events of its own to re-read; the baseline that came with the restart replaced it. */
	resync(): void {}

	#add(path: string): void {
		if (this.#files.has(path)) return;
		const i = path.lastIndexOf('/');
		const name = path.slice(i + 1);
		const dir = i < 0 ? '' : path.slice(0, i);
		this.#files.set(path, {
			path,
			name,
			dir,
			nameKey: fuzzysort.prepare(name),
			dirKey: dir === '' ? null : fuzzysort.prepare(dir),
		});
	}
}

/** A command the registry offers; `disabled` is the reason it cannot run now, else null. */
export interface Command {
	id: string;
	title: string;
	disabled: string | null;
	run(): void;
}

export interface Segment {
	text: string;
	hit: boolean;
}

/** `text` cut into runs, the characters at `indexes` marked; the runs concatenate to `text`. */
export function segments(text: string, indexes: readonly number[]): Segment[] {
	const out: Segment[] = [];
	const hit = new Set(indexes);
	let run = '';
	let on = false;
	for (let i = 0; i < text.length; i++) {
		const h = hit.has(i);
		if (h !== on && run !== '') {
			out.push({ text: run, hit: on });
			run = '';
		}
		on = h;
		run += text[i];
	}
	if (run !== '') out.push({ text: run, hit: on });
	return out;
}

export type Item =
	| { kind: 'file'; path: string; name: Segment[]; dir: string; icon: IconKind }
	| { kind: 'command'; command: Command; title: Segment[] };

export type Mode = 'files' | 'commands';

export interface PaletteView {
	open: boolean;
	mode: Mode;
	/** The input's text, `>` included. */
	query: string;
	items: readonly Item[];
	selected: number;
	/** The index holds only part of the project (the baseline walk hit its cap). */
	partial: boolean;
}

export class PaletteModel {
	#index: FileIndex;
	#commands: () => Command[];
	#onopen: (path: string) => void;
	#listeners = new Set<() => void>();
	#open = false;
	#query = '';
	#items: Item[] = [];
	#selected = 0;
	#view: PaletteView | null = null;

	constructor(index: FileIndex, commands: () => Command[], onopen: (path: string) => void) {
		this.#index = index;
		this.#commands = commands;
		this.#onopen = onopen;
	}

	get view(): PaletteView {
		this.#view ??= {
			open: this.#open,
			mode: this.#mode(),
			query: this.#query,
			items: this.#items,
			selected: this.#selected,
			partial: !this.#index.complete,
		};
		return this.#view;
	}

	subscribe(listener: () => void): () => void {
		this.#listeners.add(listener);
		return () => {
			this.#listeners.delete(listener);
		};
	}

	/** What the page gives the palette once the shell exists: the command registry and the file opener. */
	bind(commands: () => Command[], onopen: (path: string) => void): void {
		this.#commands = commands;
		this.#onopen = onopen;
	}

	/** ⌘P opens the files, ⇧⌘P the commands; either while open switches to that mode. */
	show(mode: Mode): void {
		this.#open = true;
		this.#query = mode === 'commands' ? '>' : '';
		this.#refresh();
	}

	close(): void {
		if (!this.#open) return;
		this.#open = false;
		this.#changed();
	}

	setQuery(text: string): void {
		this.#query = text;
		this.#refresh();
	}

	move(delta: number): void {
		if (this.#items.length === 0) return;
		const n = this.#items.length;
		this.#selected = (((this.#selected + delta) % n) + n) % n;
		this.#changed();
	}

	select(index: number): void {
		if (index < 0 || index >= this.#items.length || index === this.#selected) return;
		this.#selected = index;
		this.#changed();
	}

	/** Enter on the selected item. A disabled command stays listed with its reason and runs nothing. */
	choose(index = this.#selected): void {
		const item = this.#items[index];
		if (item === undefined) return;
		if (item.kind === 'command') {
			if (item.command.disabled !== null) return;
			this.close();
			item.command.run();
		} else {
			this.close();
			this.#onopen(item.path);
		}
	}

	#mode(): Mode {
		return this.#query.startsWith('>') ? 'commands' : 'files';
	}

	#refresh(): void {
		this.#items = this.#mode() === 'commands' ? this.#searchCommands(this.#query.slice(1).trim()) : this.#searchFiles(this.#query.trim());
		this.#selected = 0;
		this.#changed();
	}

	#searchFiles(q: string): Item[] {
		const files = this.#index.entries;
		if (q === '') {
			return [...files]
				.sort((a, b) => (a.path < b.path ? -1 : a.path > b.path ? 1 : 0))
				.slice(0, PALETTE_LIMIT)
				.map((f) => fileItem(f, []));
		}
		const found = fuzzysort.go(q, files as FileEntry[], {
			keys: [(f: FileEntry) => f.nameKey, (f: FileEntry) => f.dirKey],
			limit: PALETTE_LIMIT,
			threshold: THRESHOLD,
			scoreFn: (r) => {
				const name = r[0]?.score ?? 0;
				const dir = (r[1]?.score ?? 0) * DIR_WEIGHT;
				const s = Math.max(name, dir);
				return s > 0 ? s : 0;
			},
		});
		return found.map((r) => fileItem(r.obj, r[0]?.indexes ?? []));
	}

	#searchCommands(q: string): Item[] {
		const all = this.#commands();
		if (q === '') {
			return all.slice(0, PALETTE_LIMIT).map((c) => ({ kind: 'command', command: c, title: [{ text: c.title, hit: false }] }));
		}
		return fuzzysort
			.go(q, all, { key: 'title', limit: PALETTE_LIMIT, threshold: THRESHOLD })
			.map((r) => ({ kind: 'command', command: r.obj, title: segments(r.target, r.indexes) }));
	}

	#changed(): void {
		this.#view = null;
		for (const l of [...this.#listeners]) l();
	}
}

function fileItem(f: FileEntry, nameIndexes: readonly number[]): Item {
	return { kind: 'file', path: f.path, name: segments(f.name, nameIndexes), dir: f.dir, icon: fileIcon(f.path) };
}

// --- the command registry --------------------------------------------------------------

/** What the registry reads and calls; the models the project page already holds. */
export interface CommandDeps {
	workspace: { readonly id: string | null; readonly trusted?: boolean };
	build: { readonly view: Pick<BuildView, 'open' | 'mirror' | 'module' | 'variant' | 'starting'>; build(): Promise<void> };
	tasks: {
		readonly view: Pick<TasksView, 'open' | 'trusted' | 'mirror' | 'tasks' | 'starting'>;
		clearSelection(): void;
		toggle(path: string): void;
		run(): Promise<void>;
	};
	emulators: {
		readonly view: Pick<EmulatorsView, 'avds' | 'starting'> & { mirror: Pick<EmulatorsView['mirror'], 'booting' | 'stopping'> };
		start(avd: string): Promise<void>;
		stop(device: string): Promise<void>;
	};
	projects: { readonly view: Pick<ProjectsView, 'rows'>; select(id: string): Promise<void> };
	/** The project shown now; "switch project" lists the others. */
	currentProject: string;
	tree: { readonly view: { showHidden: boolean }; setShowHidden(on: boolean): void };
	density: { get(): Density; set(d: Density): void };
	focusTree(): void;
	/** The toolbar's ▶ (m2e D37): `view.disabled` is the reason it cannot run, or null. */
	run: { readonly view: { readonly disabled: string | null }; run(): Promise<void> };
	/** Opens the sessions column. */
	openSessions(): void;
}

/**
 * The commands, with the reason each cannot run now. The task and emulator lists are read
 * when the palette is searched, so a command follows the state of the page.
 */
export function buildCommands(d: CommandDeps): Command[] {
	const out: Command[] = [];
	const add = (id: string, title: string, disabled: string | null, run: () => void) => out.push({ id, title, disabled, run });

	const b = d.build.view;
	let buildWhy: string | null = null;
	if (!b.open || d.workspace.id === null) buildWhy = 'Open a project first';
	else if (d.workspace.trusted !== true) buildWhy = 'Trust this project to build it';
	else if (b.mirror.building !== null) buildWhy = 'A build is running';
	else if (b.starting) buildWhy = 'The build is starting';
	else if (b.module === '' || b.variant === '') buildWhy = 'No module and variant are selected';
	add('build', 'Build', buildWhy, () => void d.build.build());
	add('run', 'Run', d.run.view.disabled, () => void d.run.run());

	const t = d.tasks.view;
	let taskWhy: string | null = null;
	if (!t.open) taskWhy = 'Open a project first';
	else if (!t.trusted) taskWhy = 'Trust this project to list and run its tasks';
	else if (t.mirror.building !== null) taskWhy = 'A build is running';
	else if (t.starting) taskWhy = 'A run is starting';
	if (t.tasks.length === 0) {
		add('task', 'Run task…', taskWhy ?? 'The tasks are not loaded yet; open the Tasks selector', () => {});
	} else {
		for (const task of t.tasks) {
			add(`task:${task.path}`, `Run task: ${task.path}`, taskWhy, () => {
				d.tasks.clearSelection();
				d.tasks.toggle(task.path);
				void d.tasks.run();
			});
		}
	}

	const e = d.emulators.view;
	for (const avd of e.avds) {
		if (avd.running) {
			const device = avd.running.device;
			add(`emulator.stop:${avd.name}`, `Stop emulator: ${avd.name}`, e.mirror.stopping !== null ? 'An emulator is stopping' : null, () => void d.emulators.stop(device));
		} else {
			const why = e.mirror.booting !== null ? 'An emulator is booting' : e.starting ? 'An emulator is starting' : null;
			add(`emulator.start:${avd.name}`, `Start emulator: ${avd.name}`, why, () => void d.emulators.start(avd.name));
		}
	}

	for (const p of d.projects.view.rows) {
		if (p.id === d.currentProject) continue;
		add(`project:${p.id}`, `Switch project: ${p.name}`, null, () => void d.projects.select(p.id));
	}

	add('files.hidden', d.tree.view.showHidden ? 'Hide hidden files' : 'Show hidden files', null, () => d.tree.setShowHidden(!d.tree.view.showHidden));
	const dens = d.density.get();
	add('density', dens === 'compact' ? 'Density: comfortable' : 'Density: compact', null, () => d.density.set(dens === 'compact' ? 'comfortable' : 'compact'));
	add('files.focus', 'Focus the file tree', null, () => d.focusTree());
	add('sessions.show', 'Show sessions', null, () => d.openSessions());
	return out;
}
