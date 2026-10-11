// The device list and the selected device (plan m2b step 5). The list is the daemon's
// (`device.list`), read at load and when an operation of D13's trigger list ends, run by
// any client; it is never polled. The selection is a view preference shared by the
// devices panel and Logcat: one online device is the choice until the user picks another,
// and a device that left the list is no longer selected.
import type { Params, Result } from '../api/capabilities.gen';
import { CapabilityError, type CallOptions } from '../api/client';
import { failure, type Failure } from '../build/failure';
import { DEVICE_TRIGGERS, type DevicesActivity } from './mirror';

export type DeviceInfo = Result<'device.list'>['devices'][number];

export interface DevicesClient {
	call(name: 'device.list', params: Params<'device.list'>, opts?: CallOptions): Promise<Result<'device.list'>>;
}

/** What the project file preselects (ADR-0046): a running emulator's AVD or a device's serial. */
export type DevicePreselect = { avd: string } | { serial: string };

/** The device chosen by default (D19): the only running emulator, else the only online device. */
export function defaultDevice(devices: readonly DeviceInfo[]): string {
	const online = devices.filter((d) => d.online);
	const emulators = online.filter((d) => d.kind === 'emulator');
	if (emulators.length === 1) return emulators[0]!.device;
	return online.length === 1 ? online[0]!.device : '';
}

export interface DevicesView {
	devices: readonly DeviceInfo[];
	/** The selected serial; '' for none. */
	selected: string;
	/** D36: an AVD chosen in the toolbar that is not running; '' for none. The run starts it. */
	pendingAvd: string;
	/** A `device.list` failure that has a code. */
	error: Failure | null;
}

export class DevicesModel {
	#client: DevicesClient;
	#listeners = new Set<() => void>();
	#view: DevicesView | null = null;
	#off: () => void;
	#devices: DeviceInfo[] = [];
	#selected = '';
	#pendingAvd = '';
	#error: Failure | null = null;
	#gen = 0;
	// ADR-0046: the project file's device applies until the user picks one; the user's
	// own picks are announced so the file can be written from them.
	#pref: DevicePreselect | null = null;
	#touched = false;
	#choices = new Set<(serial: string) => void>();
	#avdChoices = new Set<(avd: string) => void>();

	constructor(client: DevicesClient, activity: Pick<DevicesActivity, 'onTerminal'>) {
		this.#client = client;
		this.#off = activity.onTerminal(DEVICE_TRIGGERS, () => void this.refresh());
	}

	get view(): DevicesView {
		this.#view ??= { devices: this.#devices, selected: this.#selected, pendingAvd: this.#pendingAvd, error: this.#error };
		return this.#view;
	}

	subscribe(listener: () => void): () => void {
		this.#listeners.add(listener);
		return () => {
			this.#listeners.delete(listener);
		};
	}

	/** The user's pick; `fn` hears of it, never of a default or a preselection. */
	select(serial: string): void {
		this.#touched = true;
		this.#selected = serial;
		this.#pendingAvd = '';
		this.#changed();
		if (serial !== '') for (const fn of [...this.#choices]) fn(serial);
	}

	/** D36: the user picks an AVD that is not running; no device is selected until the run starts it. */
	selectAvd(avd: string): void {
		this.#touched = true;
		this.#selected = '';
		this.#pendingAvd = avd;
		this.#changed();
		if (avd !== '') for (const fn of [...this.#avdChoices]) fn(avd);
	}

	/** `fn` hears of an AVD the user picks (the project file's `{avd}`), never of a serial. */
	subscribeAvdChoice(fn: (avd: string) => void): () => void {
		this.#avdChoices.add(fn);
		return () => {
			this.#avdChoices.delete(fn);
		};
	}

	subscribeChoice(fn: (serial: string) => void): () => void {
		this.#choices.add(fn);
		return () => {
			this.#choices.delete(fn);
		};
	}

	/** The project file's device: chosen when the list holds it online, until the user picks. */
	setPreselect(pref: DevicePreselect | null): void {
		this.#pref = pref;
		if (this.#apply()) this.#changed();
	}

	/**
	 * The project file's AVD, which `emulator.list` lists as not running (the caller checks,
	 * ADR-0046 §3), is the pending AVD until the user picks (m2e D36), so ▶ starts it. Once
	 * its emulator runs, the preselection selects its serial.
	 */
	restoreAvd(avd: string): void {
		if (this.#touched || avd === '' || this.#pendingAvd === avd) return;
		this.#selected = '';
		this.#pendingAvd = avd;
		this.#changed();
	}

	/** Selects the preselected device if it is listed and online; whether the selection changed. */
	#apply(): boolean {
		const p = this.#pref;
		if (p === null || this.#touched) return false;
		const hit = this.#devices.find((d) => d.online && ('avd' in p ? d.avdName === p.avd : d.device === p.serial));
		if (hit === undefined) return false;
		if (hit.device === this.#selected && this.#pendingAvd === '') return false;
		this.#selected = hit.device;
		this.#pendingAvd = '';
		return true;
	}

	/** `device.list`; a newer read supersedes an older one still on its way. */
	async refresh(): Promise<void> {
		const gen = ++this.#gen;
		try {
			const r = await this.#client.call('device.list', {});
			if (gen !== this.#gen) return;
			this.#devices = r.devices;
			this.#error = null;
			if (this.#selected !== '' && !r.devices.some((d) => d.device === this.#selected)) this.#selected = '';
			if (this.#selected === '' && this.#pendingAvd === '') this.#selected = defaultDevice(r.devices);
			this.#apply();
		} catch (e) {
			if (gen !== this.#gen) return;
			// A lost connection is shown by the connection state; a capability error by its code.
			if (!(e instanceof CapabilityError)) return;
			this.#error = failure(e);
		}
		this.#changed();
	}

	dispose(): void {
		this.#off();
		this.#choices.clear();
		this.#avdChoices.clear();
	}

	#changed(): void {
		this.#view = null;
		for (const l of [...this.#listeners]) l();
	}
}
