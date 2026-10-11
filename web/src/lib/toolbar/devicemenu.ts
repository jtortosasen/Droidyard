// The toolbar's device menu (m2e D36): the devices `device.list` reports (an offline one
// shown disabled), then, under "Start and run", the AVDs `emulator.list` does not report
// running. An AVD whose emulator runs is listed once, as its device. Choosing a device is
// the selection; choosing an AVD is a pending AVD that the run starts.
import type { AvdInfo } from '../devices/emulators';
import type { DeviceInfo, DevicesView } from '../devices/devices';

export interface DeviceMenuItem {
	kind: 'device' | 'avd';
	/** The serial of a device, the name of an AVD. */
	value: string;
	label: string;
	disabled: boolean;
	/** Why a disabled item cannot be chosen. */
	reason: string | null;
	current: boolean;
}

export interface DeviceMenu {
	devices: DeviceMenuItem[];
	/** Listed under "Start and run". */
	avds: DeviceMenuItem[];
}

/** A device's text: its name where it has one, then its serial. */
export function deviceText(d: DeviceInfo): string {
	const name = d.avdName ?? d.model ?? d.product ?? d.kind;
	return `${name} (${d.device})`;
}

export function deviceMenu(view: Pick<DevicesView, 'devices' | 'selected' | 'pendingAvd'>, avds: readonly AvdInfo[]): DeviceMenu {
	return {
		devices: view.devices.map((d) => ({
			kind: 'device',
			value: d.device,
			label: deviceText(d),
			disabled: !d.online,
			reason: d.online ? null : `${d.device} is ${d.state}`,
			current: view.selected === d.device,
		})),
		avds: avds
			.filter((a) => !a.running)
			.map((a) => ({
				kind: 'avd',
				value: a.name,
				label: a.name,
				disabled: false,
				reason: null,
				current: view.pendingAvd === a.name,
			})),
	};
}

export interface DeviceChoice {
	select(serial: string): void;
	selectAvd(avd: string): void;
}

/** The user's pick: a device is the selection, an AVD is held pending. A disabled item does nothing. */
export function chooseDeviceItem(model: DeviceChoice, item: DeviceMenuItem): void {
	if (item.disabled) return;
	if (item.kind === 'avd') model.selectAvd(item.value);
	else model.select(item.value);
}

/** The trigger's text. */
export function deviceTriggerText(view: Pick<DevicesView, 'devices' | 'selected' | 'pendingAvd'>): string {
	if (view.pendingAvd !== '') return `${view.pendingAvd} (not running)`;
	const d = view.devices.find((x) => x.device === view.selected);
	if (d) return d.avdName ?? d.model ?? d.product ?? d.device;
	return 'No device';
}

/** What the right panel and Logcat show for the toolbar's device. */
export function deviceLine(view: Pick<DevicesView, 'devices' | 'selected' | 'pendingAvd'>): string {
	if (view.pendingAvd !== '') return `AVD not running: ${view.pendingAvd}`;
	const d = view.devices.find((x) => x.device === view.selected);
	return d ? deviceText(d) : 'No device chosen';
}
