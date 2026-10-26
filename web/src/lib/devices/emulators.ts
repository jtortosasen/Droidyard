// The emulators panel's model (plan m2b step 5): `emulator.list` (AVDs, running or not),
// Start and Stop. *Booting* and *stopping* are the mirror's (D11), so an agent's start or
// stop shows the same and Cancel targets that operation. The list is read at load and
// when a D13 trigger ends (any client's start, stop, wipe, install, launch...), never polled.
import type { Params, Result } from '../api/capabilities.gen';
import { CapabilityError, type CallOptions } from '../api/client';
import { failure, type Failure } from '../build/failure';
import { changedSettings, draftProblems, nameProblem, settingsOf, type Draft, type Problems, type Settings } from './avdform';
import { DEVICE_TRIGGERS, type DevicesActivity, type DevicesMirror, type DevicesMirrorView } from './mirror';

export type AvdInfo = Result<'emulator.list'>['avds'][number];

export type Profile = Result<'sdk.profiles'>['profiles'][number];

type Calls =
	| 'emulator.list'
	| 'emulator.start'
	| 'emulator.stop'
	| 'emulator.create'
	| 'emulator.configure'
	| 'emulator.delete'
	| 'emulator.wipe'
	| 'sdk.profiles'
	| 'sdk.status';

/** Edit and Delete need the AVD stopped; the reason is shown beside the disabled buttons. */
export function rowActions(avd: AvdInfo, working: boolean): { edit: boolean; remove: boolean; why: string | null } {
	const running = !!avd.running;
	return {
		edit: !running && !working,
		remove: !running && !working,
		why: running ? `Edit and Delete need ${avd.name} stopped.` : null,
	};
}

export interface NewAvd {
	name: string;
	profile: string;
	image: string;
	draft: Draft;
}

export interface EmulatorsClient {
	call<N extends Calls>(name: N, params: Params<N>, opts?: CallOptions): Promise<Result<N>>;
}

export interface EmulatorsView {
	avds: readonly AvdInfo[];
	/** The AVD the project file names, when it is listed and not running: Start is its action. */
	preselected: string;
	mirror: DevicesMirrorView;
	/** View preference: boot without a window. */
	headless: boolean;
	/** Start was clicked and the daemon has not answered yet. */
	starting: boolean;
	loadError: Failure | null;
	startError: Failure | null;
	stopError: Failure | null;
	/** Device profiles and installed system images, read when New AVD opens. */
	profiles: readonly Profile[];
	images: readonly string[];
	choicesLoaded: boolean;
	choicesError: Failure | null;
	/** A create, edit, wipe or delete call is in flight. */
	working: boolean;
	createError: Failure | null;
	editError: Failure | null;
	wipeError: Failure | null;
	deleteError: Failure | null;
}

export class EmulatorsModel {
	readonly mirror: DevicesMirror;
	#client: EmulatorsClient;
	#listeners = new Set<() => void>();
	#view: EmulatorsView | null = null;
	#offs: (() => void)[] = [];
	#avds: AvdInfo[] = [];
	#headless = false;
	#starting = false;
	#loadError: Failure | null = null;
	#startError: Failure | null = null;
	#stopError: Failure | null = null;
	#profiles: Profile[] = [];
	#images: string[] = [];
	#choicesLoaded = false;
	#choicesError: Failure | null = null;
	#working = false;
	#createError: Failure | null = null;
	#editError: Failure | null = null;
	#wipeError: Failure | null = null;
	#deleteError: Failure | null = null;
	#gen = 0;
	// ADR-0046: the AVD the project file names, and the listeners told when the user starts one.
	#pref = '';
	#choices = new Set<(avd: string) => void>();

	constructor(client: EmulatorsClient, activity: Pick<DevicesActivity, 'onTerminal'>, mirror: DevicesMirror) {
		this.#client = client;
		this.mirror = mirror;
		this.#offs.push(mirror.subscribe(() => this.#changed()));
		this.#offs.push(activity.onTerminal(DEVICE_TRIGGERS, () => void this.refresh()));
	}

	get view(): EmulatorsView {
		this.#view ??= {
			avds: this.#avds,
			preselected: this.#avds.some((a) => a.name === this.#pref && !a.running) ? this.#pref : '',
			mirror: this.mirror.view,
			headless: this.#headless,
			starting: this.#starting,
			loadError: this.#loadError,
			startError: this.#startError,
			stopError: this.#stopError,
			profiles: this.#profiles,
			images: this.#images,
			choicesLoaded: this.#choicesLoaded,
			choicesError: this.#choicesError,
			working: this.#working,
			createError: this.#createError,
			editError: this.#editError,
			wipeError: this.#wipeError,
			deleteError: this.#deleteError,
		};
		return this.#view;
	}

	subscribe(listener: () => void): () => void {
		this.#listeners.add(listener);
		return () => {
			this.#listeners.delete(listener);
		};
	}

	/** The project file's AVD; one the list lacks, or one already running, preselects nothing. */
	setPreselect(avd: string | null): void {
		this.#pref = avd ?? '';
		this.#changed();
	}

	/** `fn` hears of an AVD the user starts: that is the project's AVD from then on. */
	subscribeChoice(fn: (avd: string) => void): () => void {
		this.#choices.add(fn);
		return () => {
			this.#choices.delete(fn);
		};
	}

	setHeadless(headless: boolean): void {
		this.#headless = headless;
		this.#changed();
	}

	async refresh(): Promise<void> {
		const gen = ++this.#gen;
		try {
			const r = await this.#client.call('emulator.list', {});
			if (gen !== this.#gen) return;
			this.#avds = r.avds;
			this.#loadError = null;
		} catch (e) {
			if (gen !== this.#gen) return;
			if (!(e instanceof CapabilityError)) return;
			this.#loadError = failure(e);
		}
		this.#changed();
	}

	/** `emulator.start`, refused while anything boots (the daemon would queue it behind). */
	async start(avd: string): Promise<void> {
		if (this.#starting || this.mirror.view.booting !== null) return;
		for (const fn of [...this.#choices]) fn(avd);
		this.#starting = true;
		this.#startError = null;
		this.#changed();
		try {
			await this.#client.call('emulator.start', this.#headless ? { avd, headless: true } : { avd });
		} catch (e) {
			// A lost connection leaves the boot running in the daemon; the mirror shows it.
			if (e instanceof CapabilityError) this.#startError = failure(e);
		} finally {
			this.#starting = false;
			this.#changed();
		}
	}

	/** `emulator.stop` of a running emulator's serial. */
	async stop(device: string): Promise<void> {
		if (this.mirror.view.stopping !== null) return;
		this.#stopError = null;
		this.#changed();
		try {
			await this.#client.call('emulator.stop', { device });
		} catch (e) {
			if (e instanceof CapabilityError) this.#stopError = failure(e);
		}
		this.#changed();
	}

	/** The profiles and the installed system images New AVD offers. */
	async loadChoices(): Promise<void> {
		this.#choicesError = null;
		try {
			const [p, s] = await Promise.all([this.#client.call('sdk.profiles', {}), this.#client.call('sdk.status', {})]);
			this.#profiles = p.profiles;
			this.#images = s.installed.systemImages.map((i) => i.id);
			this.#choicesLoaded = true;
		} catch (e) {
			if (e instanceof CapabilityError) {
				this.#choicesError = failure(e);
				this.#choicesLoaded = true;
			}
		}
		this.#changed();
	}

	/**
	 * `emulator.create`. A form that does not pass `draftProblems`, a name already listed or a
	 * missing profile or image is refused here, before the call; the problems come back by field.
	 */
	async create(input: NewAvd): Promise<Problems> {
		const problems: Problems = draftProblems(input.draft);
		const name = nameProblem(input.name, this.#avds.map((a) => a.name));
		if (name) problems.avd = name;
		if (input.profile === '') problems.profile = 'Choose a device profile.';
		if (input.image === '') problems.systemImage = 'Choose a system image.';
		if (Object.keys(problems).length > 0 || this.#working) return problems;
		const settings = settingsOf(input.draft);
		const params = { avd: input.name, profile: input.profile, systemImage: input.image };
		return this.#run(
			(f) => (this.#createError = f),
			async () => {
				await this.#client.call('emulator.create', Object.keys(settings).length > 0 ? { ...params, settings } : params);
				await this.refresh();
			},
		);
	}

	/** `emulator.configure` of a stopped AVD: only the fields that differ from its stored settings. */
	async configure(avd: string, draft: Draft): Promise<Problems> {
		const row = this.#avds.find((a) => a.name === avd);
		const current: Settings | undefined = row?.config?.settings;
		const problems = draftProblems(draft, current ?? {});
		if (Object.keys(problems).length > 0 || row === undefined || row.running || this.#working) return problems;
		const settings = changedSettings(draft, current);
		if (Object.keys(settings).length === 0) return problems;
		return this.#run(
			(f) => (this.#editError = f),
			async () => {
				await this.#client.call('emulator.configure', { avd, settings });
				await this.refresh();
			},
		);
	}

	/** `emulator.wipe`: the factory reset boots the AVD; refused while anything boots. */
	async wipe(avd: string): Promise<void> {
		if (this.#working || this.mirror.view.booting !== null) return;
		await this.#run(
			(f) => (this.#wipeError = f),
			async () => {
				await this.#client.call('emulator.wipe', this.#headless ? { avd, headless: true } : { avd });
			},
		);
	}

	/** `emulator.delete`, never of a running AVD. */
	async remove(avd: string): Promise<void> {
		if (this.#working || this.#avds.some((a) => a.name === avd && a.running)) return;
		await this.#run(
			(f) => (this.#deleteError = f),
			async () => {
				await this.#client.call('emulator.delete', { avd });
				await this.refresh();
			},
		);
	}

	async #run(setError: (f: Failure | null) => Failure | null, body: () => Promise<void>): Promise<Problems> {
		this.#createError = this.#editError = this.#wipeError = this.#deleteError = null;
		this.#working = true;
		this.#changed();
		try {
			await body();
		} catch (e) {
			if (e instanceof CapabilityError) setError(failure(e));
		} finally {
			this.#working = false;
			this.#changed();
		}
		return {};
	}

	dispose(): void {
		this.#choices.clear();
		for (const off of this.#offs) off();
		this.#offs = [];
	}

	#changed(): void {
		this.#view = null;
		for (const l of [...this.#listeners]) l();
	}
}
