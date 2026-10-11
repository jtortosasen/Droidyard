import { describe, expect, it } from 'vitest';
import type { CallOptions } from '../api/client';
import { activityWith, capError, liveOp, settle } from '../build/testing';
import { DevicesModel, type DevicesClient } from '../devices/devices';
import { InstallModel, type InstallClient } from '../devices/install';
import { DevicesMirror } from '../devices/mirror';
import { RunModel, type RunClient } from './run';

interface Call {
	name: string;
	params: unknown;
	opts?: CallOptions;
}

/** A daemon whose answers the test releases: a long-running call announces its operation id first. */
class Daemon {
	calls: Call[] = [];
	pending = new Map<string, { resolve: (v: unknown) => void; reject: (e: unknown) => void }>();
	hold = new Set<string>();
	answers = new Map<string, unknown>();
	errors = new Map<string, unknown>();
	call = (name: string, params: unknown, opts?: CallOptions): Promise<never> => {
		this.calls.push({ name, params, opts });
		const run = () => {
			opts?.onOperation?.(`op-${name}`);
			const err = this.errors.get(name);
			if (err !== undefined) return Promise.reject(err);
			return Promise.resolve(this.answers.get(name) ?? {});
		};
		if (!this.hold.has(name)) return run() as Promise<never>;
		return new Promise<unknown>((resolve, reject) => {
			this.pending.set(name, { resolve, reject });
			opts?.onOperation?.(`op-${name}`);
		}) as Promise<never>;
	};
	release(name: string, v?: unknown) {
		this.pending.get(name)!.resolve(v ?? this.answers.get(name) ?? {});
		this.pending.delete(name);
	}
	names() {
		return this.calls.map((c) => c.name);
	}
}

const dev = (device: string, avdName?: string) => ({ device, state: 'device', kind: 'emulator', online: true, avdName });

async function setup(opts: { trusted?: boolean; module?: string; selected?: string; installing?: boolean } = {}) {
	const act = await activityWith(opts.installing ? [liveOp('op-other', 'running', { capability: 'device.install', client: 'mcp' })] : []);
	const d = new Daemon();
	d.answers.set('device.list', { devices: [dev('A'), dev('emulator-5554', 'pixel')] });
	d.answers.set('emulator.start', { device: 'emulator-5554', avd: 'pixel', adopted: false });
	d.answers.set('build.assemble', { buildId: 'b1', outcome: 'success' });
	d.answers.set('device.install', { applicationId: 'com.x.app', apk: 'a.apk' });
	d.answers.set('device.launch', { applicationId: 'com.x.app', pid: 1, launchedAt: 't' });
	const mirror = new DevicesMirror(act.store, d as never);
	const devices = new DevicesModel(d as unknown as DevicesClient, act.store);
	await devices.refresh();
	if (opts.selected !== undefined) devices.select(opts.selected);
	const build = { view: { module: opts.module ?? ':app', variant: 'debug' }, subscribe: () => () => {} };
	const ws = { root: '/p', trusted: opts.trusted ?? true, subscribe: () => () => {} };
	const sel = { selection: () => ({ root: '/p', module: build.view.module, variant: 'debug' }), subscribe: () => () => {} };
	const install = new InstallModel(d as unknown as InstallClient, mirror, sel, devices);
	const run = new RunModel(d as unknown as RunClient, devices, build, ws, install);
	await settle();
	d.calls.length = 0;
	return { d, devices, install, run, build };
}

describe('the run sequence', () => {
	// Decision (D37, ADR-0023 §5, ADR-0046): build, then the install of that build's own
	// output by buildId, then the launch of the install's applicationId; nothing else, and
	// through InstallModel so subscribeInstalled (the project file) hears of the install.
	it('assembles, installs the build by its buildId and launches, in order', async () => {
		const t = await setup({ selected: 'A' });
		const heard: string[] = [];
		t.install.subscribeInstalled((id) => heard.push(id));
		await t.run.run();
		expect(t.d.calls.map((c) => c.name)).toEqual(['build.assemble', 'device.install', 'device.launch']);
		expect(t.d.calls[0]!.params).toEqual({ module: ':app', variant: 'debug' });
		expect(t.d.calls[1]!.params).toEqual({ device: 'A', build: { buildId: 'b1', module: ':app', variant: 'debug' } });
		expect(t.d.calls[2]!.params).toEqual({ device: 'A', applicationId: 'com.x.app' });
		expect(heard).toEqual(['com.x.app']);
		expect(t.run.view.state).toBe('done');
	});

	// Decision (D36, D37): a user's pick of an AVD after a device is a pending AVD; the
	// run starts it first, uses and selects the device the start answered, and the file's
	// preselection is the AVD.
	it('starts the pending AVD first and uses the device it answers', async () => {
		const t = await setup({ selected: 'A' });
		const avds: string[] = [];
		t.devices.subscribeAvdChoice((a) => avds.push(a));
		t.devices.selectAvd('pixel');
		expect(avds).toEqual(['pixel']);
		expect(t.devices.view.selected).toBe('');
		await t.run.run();
		expect(t.d.names()).toEqual(['emulator.start', 'build.assemble', 'device.install', 'device.launch']);
		expect(t.d.calls[0]!.params).toEqual({ avd: 'pixel' });
		expect(t.d.calls[2]!.params).toMatchObject({ device: 'emulator-5554' });
		expect(t.devices.view.selected).toBe('emulator-5554');
		expect(t.devices.view.pendingAvd).toBe('');
	});

	// Decision (ADR-0020): a failed or cancelled build is a result with an outcome, never
	// an error, and no install follows either.
	it.each([
		['failed', 'build-failed'],
		['cancelled', 'cancelled'],
	])('does not install after a build whose outcome is %s', async (outcome, state) => {
		const t = await setup({ selected: 'A' });
		t.d.answers.set('build.assemble', { buildId: 'b1', outcome });
		await t.run.run();
		expect(t.d.names()).toEqual(['build.assemble']);
		expect(t.run.view.state).toBe(state);
	});

	// Decision (D37): an operation error stops the run naming its step and code.
	it('stops on an operation error with the step and the code', async () => {
		const t = await setup({ selected: 'A' });
		t.d.errors.set('build.assemble', capError('WORKSPACE_UNTRUSTED'));
		await t.run.run();
		expect(t.d.names()).toEqual(['build.assemble']);
		expect(t.run.view.state).toBe('failed');
		expect(t.run.view.failed).toMatchObject({ step: 'build', failure: { code: 'WORKSPACE_UNTRUSTED' } });
	});

	it('stops at the install with its code and does not launch', async () => {
		const t = await setup({ selected: 'A' });
		t.d.errors.set('device.install', capError('INSTALL_FAILED'));
		await t.run.run();
		expect(t.d.names()).toEqual(['build.assemble', 'device.install']);
		expect(t.run.view.failed).toMatchObject({ step: 'install', failure: { code: 'INSTALL_FAILED' } });
	});

	// Decision (D37): ■ cancels the operation the run's own step started, by the id
	// onOperation gave (another client's running build is not touched), and nothing follows.
	it('cancels its own build operation and installs nothing', async () => {
		const t = await setup({ selected: 'A' });
		t.d.hold.add('build.assemble');
		const p = t.run.run();
		await settle();
		expect(t.run.view.state).toBe('building');
		t.run.cancel();
		await settle();
		expect(t.d.calls.filter((c) => c.name === 'operation.cancel').map((c) => c.params)).toEqual([{ operationId: 'op-build.assemble' }]);
		t.d.release('build.assemble', { buildId: 'b1', outcome: 'cancelled' });
		await p;
		expect(t.d.names()).not.toContain('device.install');
		expect(t.run.view.state).toBe('cancelled');
	});

	// Decision (D37; level 5 of step 8): a launch is short and not cancelled, so ■ is not
	// offered during it: the view says it cannot cancel and a press sends nothing. Seen
	// failing with ■ cancellable in every running state.
	it('does not offer or send a cancel during the launch', async () => {
		const t = await setup({ selected: 'A' });
		t.d.hold.add('device.launch');
		const p = t.run.run();
		await settle();
		expect(t.run.view).toMatchObject({ state: 'launching', running: true, cancellable: false });
		t.run.cancel();
		await settle();
		expect(t.d.names()).not.toContain('operation.cancel');
		t.d.release('device.launch');
		await p;
		expect(t.run.view.state).toBe('done');
	});

	// Decision (D37): ■ before the operation id is known sends the cancel when it arrives.
	it('sends a cancel pressed before the id when the id arrives', async () => {
		const t = await setup({ selected: 'A' });
		let announce!: () => void;
		let finish!: (v: unknown) => void;
		t.d.call = ((name: string, params: unknown, opts?: CallOptions) => {
			t.d.calls.push({ name, params, opts });
			if (name !== 'build.assemble') return Promise.resolve({});
			announce = () => opts?.onOperation?.('op-late');
			return new Promise((r) => (finish = r));
		}) as never;
		const p = t.run.run();
		await settle();
		t.run.cancel();
		await settle();
		expect(t.d.names()).not.toContain('operation.cancel');
		announce();
		await settle();
		expect(t.d.calls.at(-1)).toMatchObject({ name: 'operation.cancel', params: { operationId: 'op-late' } });
		finish({ buildId: 'b1', outcome: 'cancelled' });
		await p;
		expect(t.d.names()).not.toContain('device.install');
		expect(t.run.view.state).toBe('cancelled');
	});

	// Decision (D37): another client's install when the build ends stops the run with that
	// reason; the launch does not follow.
	it('stops when another client is installing as the build ends', async () => {
		const t = await setup({ selected: 'A', installing: true });
		await t.run.run();
		expect(t.d.names()).toEqual(['build.assemble']);
		expect(t.run.view.state).toBe('failed');
		expect(t.run.view.failed).toMatchObject({ step: 'install' });
		expect(t.run.view.failed!.failure.message).toMatch(/Another client/);
	});

	// Decision (D37): disabled with the reason for untrusted, no module and no device.
	it.each([
		['untrusted', { trusted: false, selected: 'A' }, /not trusted/],
		['no module', { module: '', selected: 'A' }, /module/],
		['no device', {}, /device or AVD/],
	])('is disabled with a reason when %s, and calls nothing', async (_n, o, reason) => {
		const t = await setup(o);
		expect(t.run.view.disabled).toMatch(reason);
		await t.run.run();
		expect(t.d.calls).toEqual([]);
	});

	// Decision (D37): a second ▶ while running is ignored.
	it('ignores a second run while one is in progress', async () => {
		const t = await setup({ selected: 'A' });
		t.d.hold.add('build.assemble');
		const p = t.run.run();
		await settle();
		expect(t.run.view.disabled).toMatch(/in progress/);
		await t.run.run();
		expect(t.d.names().filter((n) => n === 'build.assemble')).toHaveLength(1);
		t.d.release('build.assemble');
		await p;
	});

	// Decision (D37): dispose stops between steps; the build in flight finishes, no install starts.
	it('starts no install after dispose mid-build', async () => {
		const t = await setup({ selected: 'A' });
		t.d.hold.add('build.assemble');
		const p = t.run.run();
		await settle();
		t.run.dispose();
		t.d.release('build.assemble');
		await p;
		expect(t.d.names()).toEqual(['build.assemble']);
	});
});
