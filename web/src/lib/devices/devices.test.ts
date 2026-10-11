import { describe, expect, it } from 'vitest';
import { DevicesModel, type DevicesClient } from './devices';
import { EmulatorsModel, type EmulatorsClient } from './emulators';
import { InstallModel, type InstallClient } from './install';
import { DevicesMirror } from './mirror';
import { activityWith, FakeCalls, liveOp, settle, stateEvent } from '../build/testing';

const dev = (device: string, online = true) => ({ device, state: online ? 'device' : 'offline', kind: 'emulator', online });

async function setup(ops: ReturnType<typeof liveOp>[] = [], selection: { root: string; module: string; variant: string } | null = { root: '/p', module: ':app', variant: 'debug' }) {
	const act = await activityWith(ops);
	const calls = new FakeCalls();
	calls.on('device.list', () => ({ devices: [dev('emulator-5554')] }));
	calls.on('emulator.list', () => ({ avds: [{ name: 'a1' }] }));
	calls.on('emulator.start', () => ({ device: 'emulator-5554', avd: 'a1', adopted: false }));
	calls.on('emulator.stop', () => ({ device: 'emulator-5554' }));
	calls.on('operation.cancel', () => ({}));
	calls.on('device.install', () => ({ applicationId: 'com.x.app', apk: 'app-debug.apk' }));
	calls.on('device.launch', () => ({ applicationId: 'com.x.app', pid: null, launchedAt: 't', reason: 'crashed on start' }));
	const mirror = new DevicesMirror(act.store, calls as never);
	const devices = new DevicesModel(calls as unknown as DevicesClient, act.store);
	const emulators = new EmulatorsModel(calls as unknown as EmulatorsClient, act.store, mirror);
	const build = { sel: selection, selection: () => build.sel, subscribe: () => () => {} };
	const install = new InstallModel(calls as unknown as InstallClient, mirror, build, devices);
	await settle();
	calls.calls.length = 0;
	return { ...act, calls, mirror, devices, emulators, install, build };
}

const startOp = (id: string, over: Record<string, unknown> = {}) => liveOp(id, 'running', { capability: 'emulator.start', workspace: null, ...over });
const reads = (c: FakeCalls) => c.names().filter((n) => n === 'device.list' || n === 'emulator.list').sort();

describe('the devices mirror', () => {
	// Decision (D11): booting is any client's non-terminal emulator.start, read from the
	// store; Start is disabled and Cancel targets that operation.
	it('shows an mcp emulator.start as booting and cancels that operation', async () => {
		const t = await setup([startOp('op-m', { client: 'mcp' })]);
		expect(t.emulators.view.mirror.booting).toMatchObject({ operationId: 'op-m', client: 'mcp', state: 'running' });
		await t.emulators.start('a1');
		expect(t.calls.names()).not.toContain('emulator.start');
		await t.mirror.cancel('op-m');
		expect(t.calls.calls.at(-1)).toMatchObject({ name: 'operation.cancel', params: { operationId: 'op-m' } });
		expect(t.calls.names()).not.toContain('operation.wait');
	});

	// Decision (D11): emulator.wipe shares the boot key, so it shows as booting too.
	it('shows an emulator.wipe as booting', async () => {
		const t = await setup([startOp('op-w', { capability: 'emulator.wipe' })]);
		expect(t.emulators.view.mirror.booting?.operationId).toBe('op-w');
	});

	// Decision (D11): stopping is its own state, not booting.
	it('shows an emulator.stop as stopping, not booting', async () => {
		const t = await setup();
		stateEvent(t.watch, 'op-s', { capability: 'emulator.stop', state: 'running' }, null);
		expect(t.emulators.view.mirror.stopping).toMatchObject({ operationId: 'op-s', client: 'mcp' });
		expect(t.emulators.view.mirror.booting).toBeNull();
		await t.emulators.stop('emulator-5554');
		expect(t.calls.names()).not.toContain('emulator.stop');
	});

	// Decision (D11): installing is any client's device.install and blocks Install here.
	it('shows another client\'s device.install as installing and refuses Install', async () => {
		const t = await setup([liveOp('op-i', 'running', { capability: 'device.install', workspace: 'w1', client: 'cli' })]);
		t.devices.select('emulator-5554');
		expect(t.install.view.mirror.installing).toMatchObject({ operationId: 'op-i', client: 'cli' });
		await t.install.install();
		expect(t.calls.names()).not.toContain('device.install');
	});

	// Decision: Cancel is offered only for an operation the mirror shows.
	it('does not cancel an id it does not show', async () => {
		const t = await setup();
		await t.mirror.cancel('op-other');
		expect(t.calls.names()).toEqual([]);
	});
});

describe('D13 refreshes', () => {
	// Decision (D13): the terminal event of a trigger, by any client, reads both lists once.
	it('reads both lists once when an emulator.start ends', async () => {
		const t = await setup([startOp('op-m')]);
		stateEvent(t.watch, 'op-m', { capability: 'emulator.start', state: 'completed' }, null);
		await settle();
		expect(reads(t.calls)).toEqual(['device.list', 'emulator.list']);
	});

	// Decision (D13): a read is never its own trigger, and unrelated operations cause none.
	it('does not read when a list or a build ends', async () => {
		const t = await setup();
		for (const [i, c] of ['emulator.list', 'device.list', 'build.assemble', 'device.tap'].entries()) {
			stateEvent(t.watch, `op-${i}`, { capability: c, state: 'completed' }, null);
		}
		await settle();
		expect(reads(t.calls)).toEqual([]);
	});

	// Decision (plan §Changes): an agent's wipe, uninstall, install, launch and stop are reflected.
	it('reads on each of the other triggers', async () => {
		const t = await setup();
		const caps = ['emulator.stop', 'emulator.wipe', 'device.install', 'device.uninstall', 'device.launch'];
		for (const [i, c] of caps.entries()) {
			t.calls.calls.length = 0;
			stateEvent(t.watch, `op-${i}`, { capability: c, state: 'completed' }, null);
			await settle();
			expect(reads(t.calls), c).toEqual(['device.list', 'emulator.list']);
		}
	});
});

describe('the selected device', () => {
	// Decision: one online device is the choice; another is not auto-chosen over a pick.
	it('selects the only online device until the user picks another', async () => {
		const t = await setup();
		await t.devices.refresh();
		expect(t.devices.view.selected).toBe('emulator-5554');
		t.calls.on('device.list', () => ({ devices: [dev('emulator-5554'), dev('emulator-5556')] }));
		t.devices.select('emulator-5556');
		await t.devices.refresh();
		expect(t.devices.view.selected).toBe('emulator-5556');
	});

	// Decision: a device that left the list (an agent stopped it) is no longer selected;
	// Logcat reads this one selection.
	it('clears a selection whose device left the list', async () => {
		const t = await setup();
		await t.devices.refresh();
		t.calls.on('device.list', () => ({ devices: [] }));
		stateEvent(t.watch, 'op-x', { capability: 'emulator.stop', state: 'completed' }, null);
		await settle();
		expect(t.devices.view.selected).toBe('');
	});
});

describe('install and launch', () => {
	// Decision: install's params come from the build panel's selection and the selected
	// device, scoped to the open workspace.
	it('installs the build panel\'s module and variant on the selected device', async () => {
		const t = await setup();
		await t.devices.refresh();
		await t.install.install();
		expect(t.calls.calls.find((c) => c.name === 'device.install')).toMatchObject({
			params: { device: 'emulator-5554', build: { module: ':app', variant: 'debug' } },
			workspace: '/p',
		});
	});

	it('does not install without a workspace selection or a device', async () => {
		const t = await setup([], null);
		await t.devices.refresh();
		await t.install.install();
		expect(t.calls.names()).not.toContain('device.install');
	});

	// Decision: launch takes the install result's applicationId, and a null pid shows its reason.
	it('launches the installed applicationId and keeps the reason of a null pid', async () => {
		const t = await setup();
		await t.devices.refresh();
		await t.install.install();
		await t.install.launch();
		expect(t.calls.calls.find((c) => c.name === 'device.launch')?.params).toEqual({ device: 'emulator-5554', applicationId: 'com.x.app' });
		expect(t.install.view.launched).toMatchObject({ pid: null, reason: 'crashed on start' });
	});
});

describe('the pending AVD (m2e D36)', () => {
	// Decision (D36): an AVD picked in the toolbar is pending, the selection is empty, and a
	// refresh does not fill it with the default device; select(serial) clears the pending AVD.
	it('keeps the selection empty across a refresh while an AVD is pending, until a serial is picked', async () => {
		const t = await setup();
		t.devices.selectAvd('a1');
		expect(t.devices.view).toMatchObject({ selected: '', pendingAvd: 'a1' });
		await t.devices.refresh();
		expect(t.devices.view).toMatchObject({ selected: '', pendingAvd: 'a1' });
		t.devices.select('emulator-5554');
		expect(t.devices.view).toMatchObject({ selected: 'emulator-5554', pendingAvd: '' });
	});

	// Decision (D36, ADR-0046): the file's `{avd}` of an AVD that is not running comes back as
	// the pending AVD after a reload, not as the default device; once its emulator runs, its
	// serial is selected. Seen failing with only running devices preselected.
	it('restores a preselected AVD that is not running as pending, then selects its emulator', async () => {
		const t = await setup();
		t.devices.setPreselect({ avd: 'a2' });
		await t.devices.refresh();
		expect(t.devices.view).toMatchObject({ selected: '', pendingAvd: 'a2' });
		t.calls.on('device.list', () => ({ devices: [dev('emulator-5554'), { ...dev('emulator-5556'), avdName: 'a2' }] }));
		await t.devices.refresh();
		expect(t.devices.view).toMatchObject({ selected: 'emulator-5556', pendingAvd: '' });
	});
});
