// The toolbar's device menu (m2e D36): the devices `device.list` reports (an offline one
// shown disabled), then, under "Start and run", the AVDs `emulator.list` does not report
// running. An AVD whose emulator runs is listed once, as its device. Choosing a device is
// the selection; choosing an AVD is a pending AVD that the run starts.
import type { AvdInfo } from '../devices/emulators';
import type { DeviceInfo, DevicesView } from '../devices/devices';
import { inUseText } from '../projects/sessions';

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

/** A device's name where it has one; a remote device's (plan m2t) starts with the host that lends it. */
function deviceName(d: DeviceInfo): string {
	const name = d.avdName ?? d.model ?? d.product ?? d.kind;
	return d.host === undefined ? name : `${d.host} · ${name}`;
}

/** A device's text: its name where it has one, then its serial. */
export function deviceText(d: DeviceInfo): string {
	return `${deviceName(d)} (${d.device})`;
}

/**
 * Who holds a remote device, from `device.list`'s `occupiedBy`: "in use by anna", "in use by anna
 * (idle)"; '' for none. A device of this machine is not read here: its holder is a session of
 * this daemon, which the live session rows show (ADR-0094). A remote device's holder is on
 * another machine, and the list is all this daemon has of it (ADR-0104 §5).
 */
export function remoteHolderText(d: DeviceInfo): string {
	if (d.host === undefined || d.occupiedBy === undefined) return '';
	return inUseText({ holder: d.occupiedBy.name ?? 'another session', idle: d.occupiedBy.stale === true });
}

/** A device's text with its remote holder, when it has one. */
export function deviceLabel(d: DeviceInfo): string {
	const holder = remoteHolderText(d);
	return holder === '' ? deviceText(d) : `${deviceText(d)} — ${holder}`;
}

export function deviceMenu(view: Pick<DevicesView, 'devices' | 'selected' | 'pendingAvd'>, avds: readonly AvdInfo[]): DeviceMenu {
	return {
		devices: view.devices.map((d) => ({
			kind: 'device',
			value: d.device,
			label: deviceLabel(d),
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
	if (d) return d.host === undefined ? (d.avdName ?? d.model ?? d.product ?? d.device) : deviceName(d);
	return 'No device';
}

/** What the right panel and Logcat show for the toolbar's device. */
export function deviceLine(view: Pick<DevicesView, 'devices' | 'selected' | 'pendingAvd'>): string {
	if (view.pendingAvd !== '') return `AVD not running: ${view.pendingAvd}`;
	const d = view.devices.find((x) => x.device === view.selected);
	return d ? deviceLabel(d) : 'No device chosen';
}
