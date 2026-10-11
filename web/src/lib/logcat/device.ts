// Logcat's own device choice (plan m2e D40). Its first entry, "Toolbar device", follows the
// toolbar's choice (the devices model's selection); picking a device holds it until that
// device leaves the list of online devices, and then the panel follows the toolbar again.
// The stream is always the whole device.
import type { DeviceInfo, DevicesView } from '../devices/devices';

export interface LogcatDevices {
	view: Pick<DevicesView, 'devices' | 'selected' | 'pendingAvd'>;
	subscribe(listener: () => void): () => void;
}

export interface LogcatDeviceView {
	/** The online devices the selector lists after "Toolbar device". */
	options: readonly DeviceInfo[];
	/** The device picked here; '' while following the toolbar. */
	pick: string;
	/** The serial to stream; '' for none. */
	serial: string;
	/** The toolbar's AVD that is not running yet (D36); '' for none. Only while following. */
	pendingAvd: string;
}

export class LogcatDevice {
	#devices: LogcatDevices;
	#pick = '';
	#view: LogcatDeviceView | null = null;
	#listeners = new Set<() => void>();
	#off: () => void;

	constructor(devices: LogcatDevices) {
		this.#devices = devices;
		this.#off = devices.subscribe(() => {
			if (this.#pick !== '' && !this.#online().some((d) => d.device === this.#pick)) this.#pick = '';
			this.#changed();
		});
	}

	get view(): LogcatDeviceView {
		this.#view ??= this.#build();
		return this.#view;
	}

	subscribe(listener: () => void): () => void {
		this.#listeners.add(listener);
		return () => {
			this.#listeners.delete(listener);
		};
	}

	/** The user's pick; '' goes back to following the toolbar. A device not online is ignored. */
	select(serial: string): void {
		if (serial !== '' && !this.#online().some((d) => d.device === serial)) return;
		this.#pick = serial;
		this.#changed();
	}

	dispose(): void {
		this.#off();
		this.#listeners.clear();
	}

	#online(): DeviceInfo[] {
		return this.#devices.view.devices.filter((d) => d.online);
	}

	#build(): LogcatDeviceView {
		const v = this.#devices.view;
		const following = this.#pick === '';
		return {
			options: this.#online(),
			pick: this.#pick,
			serial: following ? v.selected : this.#pick,
			pendingAvd: following ? v.pendingAvd : '',
		};
	}

	#changed(): void {
		this.#view = null;
		for (const l of [...this.#listeners]) l();
	}
}
