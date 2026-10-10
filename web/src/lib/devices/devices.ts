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

export interface DevicesView {
	devices: readonly DeviceInfo[];
	/** The selected serial; '' for none. */
	selected: string;
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
	#error: Failure | null = null;
	#gen = 0;

	constructor(client: DevicesClient, activity: Pick<DevicesActivity, 'onTerminal'>) {
		this.#client = client;
		this.#off = activity.onTerminal(DEVICE_TRIGGERS, () => void this.refresh());
	}

	get view(): DevicesView {
		this.#view ??= { devices: this.#devices, selected: this.#selected, error: this.#error };
		return this.#view;
	}

	subscribe(listener: () => void): () => void {
		this.#listeners.add(listener);
		return () => {
			this.#listeners.delete(listener);
		};
	}

	select(serial: string): void {
		this.#selected = serial;
		this.#changed();
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
			const online = r.devices.filter((d) => d.online);
			if (this.#selected === '' && online.length === 1) this.#selected = online[0]!.device;
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
	}

	#changed(): void {
		this.#view = null;
		for (const l of [...this.#listeners]) l();
	}
}
