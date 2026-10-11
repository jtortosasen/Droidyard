import { describe, expect, it } from 'vitest';
import { DevicesModel, type DevicesClient } from '../devices/devices';
import { activityWith, FakeCalls, settle } from '../build/testing';
import { chooseDeviceItem, deviceLine, deviceMenu, deviceTriggerText } from './devicemenu';

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
