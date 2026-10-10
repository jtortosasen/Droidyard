import { describe, expect, it } from 'vitest';
import { LogcatDevice, type LogcatDevices } from './device';
import type { DeviceInfo } from '../devices/devices';

const dev = (device: string, online = true): DeviceInfo => ({ device, state: online ? 'device' : 'offline', kind: 'emulator', online }) as DeviceInfo;

class FakeDevices implements LogcatDevices {
	view = { devices: [dev('emulator-5554'), dev('emulator-5556')] as readonly DeviceInfo[], selected: 'emulator-5554', pendingAvd: '' };
	#l = new Set<() => void>();
	subscribe(l: () => void) {
		this.#l.add(l);
		return () => this.#l.delete(l);
	}
	set(over: Partial<FakeDevices['view']>) {
		this.view = { ...this.view, ...over };
		for (const l of [...this.#l]) l();
	}
}

describe('the Logcat device (m2e D40)', () => {
	// Decision (D40): "Toolbar device" follows the toolbar's choice.
	it('follows the toolbar', () => {
		const d = new FakeDevices();
		const l = new LogcatDevice(d);
		expect(l.view).toMatchObject({ serial: 'emulator-5554', pick: '' });
		d.set({ selected: 'emulator-5556' });
		expect(l.view.serial).toBe('emulator-5556');
	});

	// Decision (D40): a device picked here is held across a toolbar change.
	it('holds a pick across a toolbar change', () => {
		const d = new FakeDevices();
		const l = new LogcatDevice(d);
		l.select('emulator-5556');
		d.set({ selected: '' });
		expect(l.view).toMatchObject({ serial: 'emulator-5556', pick: 'emulator-5556' });
	});

	// Decision (D40): a pick that leaves the list is dropped for good; the panel follows
	// the toolbar again, even if the device returns.
	it('follows again when the pick leaves the list', () => {
		const d = new FakeDevices();
		const l = new LogcatDevice(d);
		l.select('emulator-5556');
		d.set({ devices: [dev('emulator-5554'), dev('emulator-5556', false)] });
		expect(l.view).toMatchObject({ serial: 'emulator-5554', pick: '' });
		d.set({ devices: [dev('emulator-5554'), dev('emulator-5556')] });
		expect(l.view.pick).toBe('');
	});

	// Decision (D36, D40): the toolbar's pending AVD shows as "AVD not running"; a held pick hides it.
	it('reports the pending AVD only while following', () => {
		const d = new FakeDevices();
		const l = new LogcatDevice(d);
		d.set({ selected: '', pendingAvd: 'a1' });
		expect(l.view).toMatchObject({ serial: '', pendingAvd: 'a1' });
		l.select('emulator-5556');
		expect(l.view).toMatchObject({ serial: 'emulator-5556', pendingAvd: '' });
	});

	it('lists only online devices and ignores a pick that is not one', () => {
		const d = new FakeDevices();
		d.set({ devices: [dev('emulator-5554'), dev('x', false)] });
		const l = new LogcatDevice(d);
		expect(l.view.options.map((o) => o.device)).toEqual(['emulator-5554']);
		l.select('x');
		expect(l.view.pick).toBe('');
	});
});
