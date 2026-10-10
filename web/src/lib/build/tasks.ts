// The Gradle tasks panel's model (plan m2b step 4): `build.tasks` of the open workspace,
// a text filter, a selection of 1-20 and Run -> `build.run {tasks}`. *Building* and Cancel
// are the build mirror's (one state shared with the build panel, D11); the result of the
// run is read by the build model on the run's end (D13), not here.
import type { Params, Result } from '../api/capabilities.gen';
import { CapabilityError, type CallOptions } from '../api/client';
import { failure, type Failure } from './failure';
import type { BuildWorkspace } from './build';
import type { BuildMirror, MirrorView } from './mirror';

/** The schema's bound on `build.run`'s tasks (D16). */
export const MAX_TASKS = 20;

export type TaskInfo = Result<'build.tasks'>['tasks'][number];

export interface TasksClient {
	call<N extends 'build.tasks' | 'build.run'>(name: N, params: Params<N>, opts?: CallOptions): Promise<Result<N>>;
}

export interface TasksView {
	open: boolean;
	mirror: MirrorView;
	tasks: readonly TaskInfo[];
	filter: string;
	/** The tasks whose path, name or description contain the filter, case-insensitively. */
	filtered: readonly TaskInfo[];
	/** The selected task paths, in the order chosen. */
	selected: readonly string[];
	/** The last selection that was refused because 20 are chosen. */
	limitReached: boolean;
	loading: boolean;
	stale: boolean;
	loadError: Failure | null;
	runError: Failure | null;
	starting: boolean;
}

export class TasksModel {
	readonly mirror: BuildMirror;
	#client: TasksClient;
	#ws: BuildWorkspace;
	#listeners = new Set<() => void>();
	#view: TasksView | null = null;
	#offs: (() => void)[] = [];

	#id: string | null = null;
	#tasks: TaskInfo[] = [];
	#filter = '';
	#selected: string[] = [];
	#limitReached = false;
	#loading = false;
	#stale = false;
	#loadError: Failure | null = null;
	#runError: Failure | null = null;
	#starting = false;
	#gen = 0;

	constructor(client: TasksClient, mirror: BuildMirror, ws: BuildWorkspace) {
		this.#client = client;
		this.mirror = mirror;
		this.#ws = ws;
		this.#offs.push(mirror.subscribe(() => this.#changed()));
		this.#offs.push(ws.subscribe(() => this.#workspaceChanged()));
		this.#workspaceChanged();
	}

	get view(): TasksView {
		this.#view ??= {
			open: this.#id !== null,
			mirror: this.mirror.view,
			tasks: this.#tasks,
			filter: this.#filter,
			filtered: this.#filtered(),
			selected: this.#selected,
			limitReached: this.#limitReached,
			loading: this.#loading,
			stale: this.#stale,
			loadError: this.#loadError,
			runError: this.#runError,
			starting: this.#starting,
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

	setFilter(filter: string): void {
		this.#filter = filter;
		this.#changed();
	}

	/** Selects or unselects a task; a 21st selection is refused. */
	toggle(path: string): void {
		const i = this.#selected.indexOf(path);
		if (i >= 0) {
			this.#selected = this.#selected.filter((p) => p !== path);
			this.#limitReached = false;
		} else if (this.#selected.length >= MAX_TASKS) {
			this.#limitReached = true;
		} else {
			this.#selected = [...this.#selected, path];
			this.#limitReached = false;
		}
		this.#changed();
	}

	clearSelection(): void {
		this.#selected = [];
		this.#limitReached = false;
		this.#changed();
	}

	/** `build.tasks` of the open workspace. */
	async load(): Promise<void> {
		if (this.#id === null) return;
		const gen = ++this.#gen;
		this.#loading = true;
		this.#changed();
		try {
			const r = await this.#client.call('build.tasks', {}, { workspace: this.#ws.root.trim() });
			if (gen !== this.#gen) return;
			this.#tasks = r.tasks;
			this.#stale = r.stale === true;
			this.#loadError = null;
			// A selected task the daemon no longer lists is dropped.
			const known = new Set(r.tasks.map((t) => t.path));
			this.#selected = this.#selected.filter((p) => known.has(p));
		} catch (e) {
			if (gen !== this.#gen) return;
			this.#loadError = failure(e);
		}
		this.#loading = false;
		this.#changed();
	}

	/** `build.run` of the selection, followed to its end only to report a refusal. */
	async run(): Promise<void> {
		if (this.#starting || this.mirror.view.building !== null || this.#selected.length === 0) return;
		const tasks = [...this.#selected] as Params<'build.run'>['tasks'];
		this.#starting = true;
		this.#runError = null;
		this.#changed();
		try {
			await this.#client.call('build.run', { tasks }, { workspace: this.#ws.root.trim() });
		} catch (e) {
			// A lost connection leaves the build running in the daemon; the mirror shows it.
			if (e instanceof CapabilityError) this.#runError = failure(e);
		} finally {
			this.#starting = false;
			this.#changed();
		}
	}

	#filtered(): TaskInfo[] {
		const f = this.#filter.trim().toLowerCase();
		if (f === '') return this.#tasks;
		return this.#tasks.filter((t) => `${t.path} ${t.description ?? ''}`.toLowerCase().includes(f));
	}

	#workspaceChanged(): void {
		const id = this.#ws.id;
		if (id === this.#id) return;
		this.#id = id;
		this.#tasks = [];
		this.#selected = [];
		this.#limitReached = false;
		this.#loadError = null;
		this.#runError = null;
		this.#stale = false;
		this.#loading = false;
		this.#gen++;
		this.#changed();
		if (id !== null) void this.load();
	}

	#changed(): void {
		this.#view = null;
		for (const l of [...this.#listeners]) l();
	}
}
