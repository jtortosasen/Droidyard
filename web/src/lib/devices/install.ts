// Install and launch (plan m2b step 5). `device.install {device, build: {module, variant}}`
// takes the module and variant the build panel has chosen and the device the devices panel
// has selected, in the open workspace; `device.launch {device, applicationId}` takes the
// applicationId from the install's result (the field may be typed: an agent may have
// installed it). A `null` pid is shown with its `reason`. *Installing* is the mirror's
// (D11), so another client's install disables Install here and offers Cancel.
//
// The application id shown (m2c D20): what the user typed; else the newest build's output
// for the chosen module and variant; else the project file's, which is written after each
// install this tab makes (ADR-0046). The last two only fill the field: nothing is launched
// or read from the device until a click.
import type { Params, Result } from '../api/capabilities.gen';
import { CapabilityError, type CallOptions } from '../api/client';
import { failure, type Failure } from '../build/failure';
import type { DevicesMirror, DevicesMirrorView } from './mirror';

type Calls = 'device.install' | 'device.launch';

export interface InstallClient {
	call<N extends Calls>(name: N, params: Params<N>, opts?: CallOptions): Promise<Result<N>>;
}

/** What the build panel has chosen, and the workspace root that scopes `device.install`. */
export interface BuildSelection {
	selection(): { root: string; module: string; variant: string } | null;
	subscribe(listener: () => void): () => void;
	/** D20's first source; a build selection without one falls through to the file's. */
	output?: BuildOutput;
}

/** The newest build's output application id for a module and variant, if it has one. */
export type BuildOutput = (module: string, variant: string) => string | undefined;

export interface DeviceSelection {
	view: { selected: string };
	subscribe(listener: () => void): () => void;
}

export interface InstallView {
	mirror: DevicesMirrorView;
	/** The selected device serial; '' for none. */
	device: string;
	/** The build panel's module and variant; null without an open workspace. */
	build: { module: string; variant: string } | null;
	installed: Result<'device.install'> | null;
	/** What Launch and the app logs use: typed, else the build's output, else the file's. */
	applicationId: string;
	launched: Result<'device.launch'> | null;
	installing: boolean;
	launching: boolean;
	installError: Failure | null;
	launchError: Failure | null;
}

export class InstallModel {
	readonly mirror: DevicesMirror;
	#client: InstallClient;
	#build: BuildSelection;
	#devices: DeviceSelection;
	#listeners = new Set<() => void>();
	#view: InstallView | null = null;
	#offs: (() => void)[] = [];
	#installed: Result<'device.install'> | null = null;
	/** What the user typed; null follows D20's other sources. */
	#typed: string | null = null;
	/** The project file's application id, or the one this tab's last install reported. */
	#stored = '';
	#installedHere = false;
	#installs = new Set<(applicationId: string) => void>();
	#launched: Result<'device.launch'> | null = null;
	#installing = false;
	#launching = false;
	#installError: Failure | null = null;
	#launchError: Failure | null = null;

	constructor(client: InstallClient, mirror: DevicesMirror, build: BuildSelection, devices: DeviceSelection) {
		this.#client = client;
		this.mirror = mirror;
		this.#build = build;
		this.#devices = devices;
		this.#offs.push(mirror.subscribe(() => this.#changed()));
		this.#offs.push(build.subscribe(() => this.#changed()));
		this.#offs.push(devices.subscribe(() => this.#changed()));
	}

	get view(): InstallView {
		if (this.#view === null) {
			const b = this.#build.selection();
			this.#view = {
				mirror: this.mirror.view,
				device: this.#devices.view.selected,
				build: b && b.module !== '' && b.variant !== '' ? { module: b.module, variant: b.variant } : null,
				installed: this.#installed,
				applicationId: this.#applicationId(b),
				launched: this.#launched,
				installing: this.#installing,
				launching: this.#launching,
				installError: this.#installError,
				launchError: this.#launchError,
			};
		}
		return this.#view;
	}

	subscribe(listener: () => void): () => void {
		this.#listeners.add(listener);
		return () => {
			this.#listeners.delete(listener);
		};
	}

	/** Typing replaces D20's sources; emptied, the field follows them again. */
	setApplicationId(id: string): void {
		this.#typed = id === '' ? null : id;
		this.#changed();
	}

	/** The project file's application id (ADR-0046); an install this tab made is newer than it. */
	setStoredApplicationId(id: string): void {
		if (this.#installedHere) return;
		this.#stored = id;
		this.#changed();
	}

	/** `fn` hears of the application id of each install this tab makes. */
	subscribeInstalled(fn: (applicationId: string) => void): () => void {
		this.#installs.add(fn);
		return () => {
			this.#installs.delete(fn);
		};
	}

	#applicationId(b: { module: string; variant: string } | null): string {
		if (this.#typed !== null) return this.#typed;
		const out = b && b.module !== '' && b.variant !== '' ? this.#build.output?.(b.module, b.variant) : undefined;
		return out ?? this.#stored;
	}

	/**
	 * `device.install`. Without an argument: the build panel's module and variant on the
	 * selected device. With one (the toolbar's run, m2e D37): that device and that build's
	 * own output by `buildId` (ADR-0023 §5). Returns the result, or null when a guard
	 * refused or the call failed (`view.installError` then says why, for a failure).
	 */
	async install(explicit?: {
		device: string;
		build: { buildId: string; module: string; variant: string };
		onOperation?: (operationId: string) => void;
	}): Promise<Result<'device.install'> | null> {
		const b = this.#build.selection();
		const device = explicit?.device ?? this.#devices.view.selected;
		const build = explicit?.build ?? (b === null ? null : { module: b.module, variant: b.variant });
		if (this.#installing || this.mirror.view.installing !== null || b === null || device === '' || build === null || build.module === '' || build.variant === '') return null;
		this.#installing = true;
		this.#installError = null;
		this.#changed();
		try {
			const r = await this.#client.call('device.install', { device, build }, { workspace: b.root.trim(), onOperation: explicit?.onOperation });
			this.#installed = r;
			this.#typed = null;
			this.#stored = r.applicationId;
			this.#installedHere = true;
			for (const fn of [...this.#installs]) fn(r.applicationId);
			this.#launched = null;
			this.#launchError = null;
			return r;
		} catch (e) {
			// A lost connection leaves the install running in the daemon; the mirror shows it.
			if (e instanceof CapabilityError) this.#installError = failure(e);
			return null;
		} finally {
			this.#installing = false;
			this.#changed();
		}
	}

	/** `device.launch`; with an argument (the run), that device and application id. Null on a guard or a failure. */
	async launch(explicit?: { device: string; applicationId: string }): Promise<Result<'device.launch'> | null> {
		const device = explicit?.device ?? this.#devices.view.selected;
		const applicationId = (explicit?.applicationId ?? this.view.applicationId).trim();
		if (this.#launching || device === '' || applicationId === '') return null;
		this.#launching = true;
		this.#launchError = null;
		this.#changed();
		try {
			this.#launched = await this.#client.call('device.launch', { device, applicationId });
			return this.#launched;
		} catch (e) {
			this.#launched = null;
			if (e instanceof CapabilityError) this.#launchError = failure(e);
			return null;
		} finally {
			this.#launching = false;
			this.#changed();
		}
	}

	dispose(): void {
		this.#installs.clear();
		for (const off of this.#offs) off();
		this.#offs = [];
	}

	#changed(): void {
		this.#view = null;
		for (const l of [...this.#listeners]) l();
	}
}
