import { describe, expect, it } from 'vitest';
import { DevicesModel, type DevicesClient } from '../devices/devices';
import { activityWith, FakeCalls, settle } from '../build/testing';
import { chooseDeviceItem, deviceLine, deviceMenu, deviceTriggerText, remoteHolderText } from './devicemenu';

const dev = (device: string, over: Record<string, unknown> = {}) => ({ device, state: 'device', kind: 'emulator', online: true, ...over });

async function setup(devices: unknown[]) {
	const act = await activityWith([]);
	const calls = new FakeCalls();
	calls.on('device.list', () => ({ devices }));
	const model = new DevicesModel(calls as unknown as DevicesClient, act.store);
	await model.refresh();
	await settle();
	return model;
}

describe('the device menu (D36)', () => {
	// Decision: devices first, then only the AVDs that are not running; an AVD whose
	// emulator runs is listed once, as its device, never again under "Start and run".
	it('lists the devices, then the AVDs not running, an AVD running once as its device', async () => {
		const m = await setup([dev('emulator-5554', { avdName: 'a1' }), dev('R58M', { kind: 'physical', model: 'Pixel' })]);
		const menu = deviceMenu(m.view, [{ name: 'a1', running: { device: 'emulator-5554' } }, { name: 'a2' }] as never);
		expect(menu.devices.map((i) => i.value)).toEqual(['emulator-5554', 'R58M']);
		expect(menu.avds.map((i) => i.value)).toEqual(['a2']);
		expect([...menu.devices, ...menu.avds].filter((i) => i.label.includes('a1'))).toHaveLength(1);
	});

	// Decision: a device that is not `device` state is listed and cannot be chosen.
	it('lists an offline device disabled with its state, and choosing it does nothing', async () => {
		const m = await setup([dev('emulator-5556', { online: false, state: 'offline' })]);
		const item = deviceMenu(m.view, []).devices[0]!;
		expect(item).toMatchObject({ disabled: true, reason: 'emulator-5556 is offline' });
		chooseDeviceItem(m, item);
		expect(m.view.selected).toBe('');
	});

	// Decision: a device pick is the selection (a serial); an AVD pick is pending, empties
	// the selection, and is announced as an AVD choice (the file's {avd}), not as a serial.
	it('holds an AVD pick pending, empties the selection and announces it as an AVD', async () => {
		const m = await setup([dev('emulator-5554', { avdName: 'a1' })]);
		expect(m.view.selected).toBe('emulator-5554');
		const serials: string[] = [];
		const avds: string[] = [];
		m.subscribeChoice((s) => serials.push(s));
		m.subscribeAvdChoice((a) => avds.push(a));
		const avdItem = deviceMenu(m.view, [{ name: 'a2' }] as never).avds[0]!;
		chooseDeviceItem(m, avdItem);
		expect(m.view).toMatchObject({ selected: '', pendingAvd: 'a2' });
		expect(avds).toEqual(['a2']);
		expect(serials).toEqual([]);
		expect(deviceMenu(m.view, [{ name: 'a2' }] as never).avds[0]!.current).toBe(true);
		expect(deviceTriggerText(m.view)).toBe('a2 (not running)');
		expect(deviceLine(m.view)).toBe('AVD not running: a2');
		chooseDeviceItem(m, deviceMenu(m.view, []).devices[0]!);
		expect(m.view).toMatchObject({ selected: 'emulator-5554', pendingAvd: '' });
		expect(serials).toEqual(['emulator-5554']);
		expect(deviceLine(m.view)).toBe('a1 (emulator-5554)');
	});
});

describe('remote devices in the menu (plan m2t step 7)', () => {
	const remote = (over: Record<string, unknown> = {}) =>
		dev('qa-lab/R58M', { kind: 'physical', model: 'Pixel 7', host: 'qa-lab', ...over });

	// Decision: a remote device names its host before its model, so the person knows whose
	// phone it is, and its serial is the id the host's name prefixes.
	it('names the host and the model, and the id', async () => {
		const m = await setup([remote()]);
		expect(deviceMenu(m.view, []).devices[0]!.label).toBe('qa-lab · Pixel 7 (qa-lab/R58M)');
		expect(deviceTriggerText(m.view)).toBe('qa-lab · Pixel 7');
		expect(deviceLine(m.view)).toBe('qa-lab · Pixel 7 (qa-lab/R58M)');
	});

	// Decision (ADR-0104 §5): the holder of a remote device is on another machine, so
	// `occupiedBy` is all this daemon knows; idle is said.
	it('says who holds a remote device, and when the holder is idle', async () => {
		const held = await setup([remote({ occupiedBy: { name: 'anna', since: 't' } })]);
		expect(deviceMenu(held.view, []).devices[0]!.label).toBe('qa-lab · Pixel 7 (qa-lab/R58M) — in use by anna');
		expect(deviceLine(held.view)).toContain('in use by anna');
		const idle = await setup([remote({ occupiedBy: { name: 'anna', since: 't', stale: true } })]);
		expect(remoteHolderText(idle.view.devices[0]!)).toBe('in use by anna (idle)');
		const nameless = await setup([remote({ occupiedBy: { since: 't' } })]);
		expect(remoteHolderText(nameless.view.devices[0]!)).toBe('in use by another session');
	});

	// Decision: a device of this machine keeps its text: the session rows say who holds it
	// (ADR-0094), and saying it twice would be noise.
	it('leaves a local device without a holder text', async () => {
		const m = await setup([dev('R58M', { kind: 'physical', model: 'Pixel', occupiedBy: { name: 'claude-code', since: 't' } })]);
		expect(deviceMenu(m.view, []).devices[0]!.label).toBe('Pixel (R58M)');
		expect(remoteHolderText(m.view.devices[0]!)).toBe('');
	});
});
