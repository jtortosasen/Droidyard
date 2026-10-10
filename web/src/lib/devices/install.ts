// Install and launch (plan m2b step 5). `device.install {device, build: {module, variant}}`
// takes the module and variant the build panel has chosen and the device the devices panel
// has selected, in the open workspace; `device.launch {device, applicationId}` takes the
// applicationId from the install's result (the field may be typed: an agent may have
// installed it). A `null` pid is shown with its `reason`. *Installing* is the mirror's
// (D11), so another client's install disables Install here and offers Cancel.
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
}

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
	#applicationId = '';
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
				applicationId: this.#applicationId,
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

	setApplicationId(id: string): void {
		this.#applicationId = id;
		this.#changed();
	}

	async install(): Promise<void> {
		const b = this.#build.selection();
		const device = this.#devices.view.selected;
		if (this.#installing || this.mirror.view.installing !== null || b === null || device === '' || b.module === '' || b.variant === '') return;
		this.#installing = true;
		this.#installError = null;
		this.#changed();
		try {
			const r = await this.#client.call(
				'device.install',
				{ device, build: { module: b.module, variant: b.variant } },
				{ workspace: b.root.trim() },
			);
			this.#installed = r;
			this.#applicationId = r.applicationId;
			this.#launched = null;
			this.#launchError = null;
		} catch (e) {
			// A lost connection leaves the install running in the daemon; the mirror shows it.
			if (e instanceof CapabilityError) this.#installError = failure(e);
		} finally {
			this.#installing = false;
			this.#changed();
		}
	}

	async launch(): Promise<void> {
		const device = this.#devices.view.selected;
		const applicationId = this.#applicationId.trim();
		if (this.#launching || device === '' || applicationId === '') return;
		this.#launching = true;
		this.#launchError = null;
		this.#changed();
		try {
			this.#launched = await this.#client.call('device.launch', { device, applicationId });
		} catch (e) {
			this.#launched = null;
			if (e instanceof CapabilityError) this.#launchError = failure(e);
		} finally {
			this.#launching = false;
			this.#changed();
		}
	}

	dispose(): void {
		for (const off of this.#offs) off();
		this.#offs = [];
	}

	#changed(): void {
		this.#view = null;
		for (const l of [...this.#listeners]) l();
	}
}
