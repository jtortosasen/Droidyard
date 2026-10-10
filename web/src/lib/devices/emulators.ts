// The emulators panel's model (plan m2b step 5): `emulator.list` (AVDs, running or not),
// Start and Stop. *Booting* and *stopping* are the mirror's (D11), so an agent's start or
// stop shows the same and Cancel targets that operation. The list is read at load and
// when a D13 trigger ends (any client's start, stop, wipe, install, launch...), never polled.
import type { Params, Result } from '../api/capabilities.gen';
import { CapabilityError, type CallOptions } from '../api/client';
import { failure, type Failure } from '../build/failure';
import { DEVICE_TRIGGERS, type DevicesActivity, type DevicesMirror, type DevicesMirrorView } from './mirror';

export type AvdInfo = Result<'emulator.list'>['avds'][number];

type Calls = 'emulator.list' | 'emulator.start' | 'emulator.stop';

export interface EmulatorsClient {
	call<N extends Calls>(name: N, params: Params<N>, opts?: CallOptions): Promise<Result<N>>;
}

export interface EmulatorsView {
	avds: readonly AvdInfo[];
	mirror: DevicesMirrorView;
	/** View preference: boot without a window. */
	headless: boolean;
	/** Start was clicked and the daemon has not answered yet. */
	starting: boolean;
	loadError: Failure | null;
	startError: Failure | null;
	stopError: Failure | null;
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
	#gen = 0;

	constructor(client: EmulatorsClient, activity: Pick<DevicesActivity, 'onTerminal'>, mirror: DevicesMirror) {
		this.#client = client;
		this.mirror = mirror;
		this.#offs.push(mirror.subscribe(() => this.#changed()));
		this.#offs.push(activity.onTerminal(DEVICE_TRIGGERS, () => void this.refresh()));
	}

	get view(): EmulatorsView {
		this.#view ??= {
			avds: this.#avds,
			mirror: this.mirror.view,
			headless: this.#headless,
			starting: this.#starting,
			loadError: this.#loadError,
			startError: this.#startError,
			stopError: this.#stopError,
		};
		return this.#view;
	}

	subscribe(listener: () => void): () => void {
		this.#listeners.add(listener);
		return () => {
			this.#listeners.delete(listener);
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

	dispose(): void {
		for (const off of this.#offs) off();
		this.#offs = [];
	}

	#changed(): void {
		this.#view = null;
		for (const l of [...this.#listeners]) l();
	}
}
