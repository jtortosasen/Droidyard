// The build panel's model (plan m2b step 3, D10-D13): the workspace's modules and
// variants, Build, and the last build's result and problems. It shows daemon state and
// keeps no copy of it:
//   - *building* is read from the activity store through the mirror, so a build any client
//     started shows, and Build is disabled while one is queued or running;
//   - the last result and its problems are read from the daemon (`build.lastResult`, then
//     `build.errors {buildId}`) at load and when a `build.assemble` or `build.run` of this
//     workspace ends, whoever ran it. Those reads are not in the trigger list, so a read is
//     never its own trigger and nothing polls;
//   - its own `build.assemble` call is followed to its end only to report a refusal
//     (`WORKSPACE_UNTRUSTED`...); its result is read back the same way as any other client's.
import type { Params, Result } from '../api/capabilities.gen';
import { CapabilityError, type CallOptions } from '../api/client';
import type { Operation } from '../ops/activity';
import { failure, type Failure } from './failure';
import { BUILD_CAPABILITIES, BuildMirror, type MirrorActivity, type MirrorClient, type MirrorProgress, type MirrorView } from './mirror';

type Calls =
	| 'project.modules'
	| 'project.variants'
	| 'build.assemble'
	| 'build.lastResult'
	| 'build.errors';

export interface BuildClient extends MirrorClient {
	call<N extends Calls | 'operation.cancel'>(name: N, params: Params<N>, opts?: CallOptions): Promise<Result<N>>;
}

export interface BuildActivity extends MirrorActivity {
	onTerminal(capabilities: readonly string[], fn: (op: Operation) => void): () => void;
}

export interface BuildWorkspace {
	readonly id: string | null;
	readonly root: string;
	/** Whether the open workspace is trusted; a grant makes the project's reads possible. */
	readonly trusted?: boolean;
	/** The Gradle root the daemon resolves (ADR-0075); a change of it makes the project's reads stale. */
	readonly gradleRoot?: string;
	subscribe(listener: () => void): () => void;
}

export type LastBuild = Result<'build.lastResult'>;

/** What the project file preselects (ADR-0046); a value the daemon's lists lack is ignored. */
export interface BuildPreselect {
	module?: string;
	variant?: string;
}

/** The module chosen: the preselected one if listed, else `:app` (D19), else the first real module. */
export function pickModule(modules: readonly string[], want?: string): string {
	if (want !== undefined && modules.includes(want)) return want;
	return modules.includes(':app') ? ':app' : (modules.find((p) => p !== ':') ?? modules[0] ?? '');
}

/** The variant chosen: the preselected one if listed, else `debug` (D19), else the first. */
export function pickVariant(variants: readonly string[], want?: string): string {
	if (want !== undefined && variants.includes(want)) return want;
	return variants.includes('debug') ? 'debug' : (variants[0] ?? '');
}
export type Problems = Result<'build.errors'>;

export interface BuildView {
	/** A workspace is open. */
	open: boolean;
	mirror: MirrorView;
	modules: readonly string[];
	variants: readonly string[];
	module: string;
	variant: string;
	/** The newest finished build; null with `noBuild` when there is none. */
	last: LastBuild | null;
	problems: Problems | null;
	/** `BUILD_NOT_FOUND` at the last read: there is no build in this workspace yet. */
	noBuild: boolean;
	loadError: Failure | null;
	startError: Failure | null;
	/** Build was clicked and the daemon has not answered yet. */
	starting: boolean;
}

export class BuildModel {
	readonly mirror: BuildMirror;
	#client: BuildClient;
	#ws: BuildWorkspace;
	#listeners = new Set<() => void>();
	#view: BuildView | null = null;
	#offs: (() => void)[] = [];

	#id: string | null = null;
	#modules: string[] = [];
	#variants: string[] = [];
	#module = '';
	#variant = '';
	#last: LastBuild | null = null;
	#problems: Problems | null = null;
	#noBuild = false;
	#loadError: Failure | null = null;
	#startError: Failure | null = null;
	#starting = false;
	#gen = 0; // the newest read of the last result; an older one is dropped
	#trusted = false;
	#gradleRoot: string | undefined;
	#projectGen = 0;
	// ADR-0046: what the project file preselects, applied until the user chooses something,
	// and the listeners told of the user's own choices (the file is written from those).
	#pref: BuildPreselect | null = null;
	#touched = false;
	#choices = new Set<(choice: { module: string; variant: string }) => void>();

	constructor(client: BuildClient, activity: BuildActivity, progress: MirrorProgress, ws: BuildWorkspace) {
		this.#client = client;
		this.#ws = ws;
		this.mirror = new BuildMirror(activity, progress, client, () => ws.id);
		this.#offs.push(this.mirror.subscribe(() => this.#changed()));
		// D13: only the end of a build of this workspace, by any client, causes reads.
		this.#offs.push(
			activity.onTerminal(BUILD_CAPABILITIES, (op) => {
				if (this.#id !== null && op.workspace === this.#id) void this.reload();
			}),
		);
		this.#offs.push(ws.subscribe(() => this.#workspaceChanged()));
		this.#workspaceChanged();
	}

	get view(): BuildView {
		this.#view ??= {
			open: this.#id !== null,
			mirror: this.mirror.view,
			modules: this.#modules,
			variants: this.#variants,
			module: this.#module,
			variant: this.#variant,
			last: this.#last,
			problems: this.#problems,
			noBuild: this.#noBuild,
			loadError: this.#loadError,
			startError: this.#startError,
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

	/** Calls `fn` with the module and variant after each change the user makes, never for a default. */
	subscribeChoice(fn: (choice: { module: string; variant: string }) => void): () => void {
		this.#choices.add(fn);
		return () => {
			this.#choices.delete(fn);
		};
	}

	/**
	 * The project file's module and variant (ADR-0046): used where the daemon's lists hold
	 * them, until the user chooses. Safe before or after the lists load.
	 */
	setPreselect(pref: BuildPreselect | null): void {
		this.#pref = pref;
		if (this.#touched || this.#id === null || this.#modules.length === 0) return;
		const want = pref?.module;
		if (want !== undefined && want !== this.#module && this.#modules.includes(want)) {
			void this.#chooseModule(want);
		} else if (want !== undefined && want === this.#module && this.#variants.length > 0) {
			const variant = pickVariant(this.#variants, pref?.variant);
			if (variant !== this.#variant) {
				this.#variant = variant;
				this.#changed();
			}
		}
	}

	dispose(): void {
		for (const off of this.#offs) off();
		this.#offs = [];
		this.#choices.clear();
		this.mirror.dispose();
	}

	#workspaceChanged(): void {
		const id = this.#ws.id;
		const trusted = this.#ws.trusted === true;
		const granted = trusted && !this.#trusted;
		this.#trusted = trusted;
		const gradleRoot = this.#ws.gradleRoot;
		const moved = gradleRoot !== this.#gradleRoot;
		this.#gradleRoot = gradleRoot;
		if (id === this.#id && !moved) {
			// Trust granted from the UI (ADR-0045 §5): what the untrusted workspace refused is
			// read again; no event carries trust.
			if (granted && id !== null) {
				void this.loadProject();
				void this.reload();
			}
			return;
		}
		this.#id = id;
		this.#modules = [];
		this.#variants = [];
		this.#module = '';
		this.#variant = '';
		this.#touched = false;
		this.#last = null;
		this.#problems = null;
		this.#noBuild = false;
		this.#loadError = null;
		this.#startError = null;
		this.#gen++;
		this.#projectGen++;
		this.mirror.changed();
		this.#changed();
		if (id !== null) {
			void this.loadProject();
			void this.reload();
		}
	}

	/** `project.modules`, then the variants of the chosen module. */
	async loadProject(): Promise<void> {
		const gen = ++this.#projectGen;
		const root = this.#ws.root.trim();
		try {
			const r = await this.#client.call('project.modules', {}, { workspace: root });
			if (gen !== this.#projectGen) return;
			this.#modules = r.modules.map((m) => m.path);
			this.#module = pickModule(this.#modules, this.#touched ? undefined : this.#pref?.module);
			this.#loadError = null;
			this.#changed();
			await this.#loadVariants(gen);
		} catch (e) {
			if (gen !== this.#projectGen) return;
			this.#loadError = failure(e);
			this.#changed();
		}
	}

	/** The user chose a module: its variants are read, and the pair is the user's choice. */
	async selectModule(module: string): Promise<void> {
		this.#touched = true;
		await this.#chooseModule(module);
		if (this.#module === module && this.#variant !== '') this.#announce();
	}

	selectVariant(variant: string): void {
		this.#touched = true;
		this.#variant = variant;
		this.#changed();
		if (this.#module !== '' && variant !== '') this.#announce();
	}

	#announce(): void {
		for (const fn of [...this.#choices]) fn({ module: this.#module, variant: this.#variant });
	}

	async #chooseModule(module: string): Promise<void> {
		this.#module = module;
		this.#variants = [];
		this.#variant = '';
		this.#changed();
		const gen = ++this.#projectGen;
		try {
			await this.#loadVariants(gen);
		} catch (e) {
			if (gen === this.#projectGen) {
				this.#loadError = failure(e);
				this.#changed();
			}
		}
	}

	async #loadVariants(gen: number): Promise<void> {
		if (this.#module === '') return;
		const module = this.#module;
		const r = await this.#client.call('project.variants', { module }, { workspace: this.#ws.root.trim() });
		if (gen !== this.#projectGen || module !== this.#module) return;
		this.#variants = r.modules.find((m) => m.module === module)?.variants ?? [];
		const pref = this.#touched || this.#pref?.module !== module ? undefined : this.#pref?.variant;
		this.#variant = pickVariant(this.#variants, pref);
		this.#loadError = null;
		this.#changed();
	}

	/** `build.assemble` for the chosen module and variant. */
	async build(): Promise<void> {
		if (this.#starting || this.mirror.view.building !== null || this.#module === '' || this.#variant === '') return;
		this.#starting = true;
		this.#startError = null;
		this.#changed();
		try {
			await this.#client.call('build.assemble', { module: this.#module, variant: this.#variant }, { workspace: this.#ws.root.trim() });
		} catch (e) {
			// A lost connection leaves the build running in the daemon; the mirror shows it.
			if (e instanceof CapabilityError) this.#startError = failure(e);
		} finally {
			this.#starting = false;
			this.#changed();
		}
	}

	/** The last result, then its problems: what the daemon holds, read when it may have changed. */
	async reload(): Promise<void> {
		if (this.#id === null) return;
		const gen = ++this.#gen;
		const root = this.#ws.root.trim();
		let last: LastBuild;
		try {
			last = await this.#client.call('build.lastResult', {}, { workspace: root });
		} catch (e) {
			if (gen !== this.#gen) return;
			this.#last = null;
			this.#problems = null;
			if (e instanceof CapabilityError && e.code === 'BUILD_NOT_FOUND') {
				this.#noBuild = true;
				this.#loadError = null;
			} else {
				this.#noBuild = false;
				this.#loadError = failure(e);
			}
			this.#changed();
			return;
		}
		if (gen !== this.#gen) return;
		let problems: Problems | null = null;
		let error: Failure | null = null;
		try {
			problems = await this.#client.call('build.errors', { buildId: last.buildId }, { workspace: root });
		} catch (e) {
			if (!(e instanceof CapabilityError && e.code === 'BUILD_NOT_FOUND')) error = failure(e);
		}
		if (gen !== this.#gen) return;
		this.#last = last;
		this.#problems = problems;
		this.#noBuild = false;
		this.#loadError = error;
		this.#changed();
	}

	#changed(): void {
		this.#view = null;
		for (const l of [...this.#listeners]) l();
	}
}
