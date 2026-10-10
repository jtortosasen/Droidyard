// The project file (ADR-0046) and the application id (m2c D20), tested through the real
// models over a fake daemon: the promise is what the user sees and what reaches the disk,
// not the shape of the helpers.
import { describe, expect, it } from 'vitest';
import { BuildModel, type BuildClient } from '../build/build';
import { DevicesModel, type DevicesClient } from '../devices/devices';
import { EmulatorsModel, type EmulatorsClient } from '../devices/emulators';
import { InstallModel, type InstallClient } from '../devices/install';
import { DevicesMirror } from '../devices/mirror';
import { activityWith, capError, FakeCalls, hubStarted, settle } from '../build/testing';
import { bindProjectFile } from './projectbind';
import { applyPatch, parseProjectFile, PROJECT_FILE, ProjectFileModel, type ProjectFileClient } from './projectfile';

class FakeWs {
	id: string | null = 'w1';
	root = '/p';
	trusted = true;
	#l = new Set<() => void>();
	subscribe(l: () => void) {
		this.#l.add(l);
		return () => this.#l.delete(l);
	}
	notify() {
		for (const l of this.#l) l();
	}
}

interface Opts {
	/** The file's text; null for no file. */
	file?: string | null;
	readError?: Error;
	writeError?: Error;
	modules?: string[];
	variants?: Record<string, string[]>;
	devices?: Record<string, unknown>[];
	avds?: Record<string, unknown>[];
	lastOutputs?: Record<string, unknown>[] | null;
	/** The daemon answers the project's lists only after the file was read and applied. */
	modulesAfterFile?: boolean;
}

const dev = (device: string, over: Record<string, unknown> = {}) => ({ device, state: 'device', kind: 'emulator', online: true, ...over });

async function setup(o: Opts = {}) {
	const act = await activityWith();
	const { hub } = await hubStarted();
	const calls = new FakeCalls();
	const disk = { content: o.file ?? null as string | null, writes: [] as string[] };
	const gate = { read: null as Promise<void> | null };
	let fileRead!: () => void;
	const fileDone = new Promise<void>((r) => (fileRead = r));
	calls.on('project.modules', async () => {
		if (o.modulesAfterFile) {
			await fileDone;
			await settle();
		}
		return { modules: (o.modules ?? [':app']).map((path) => ({ path, name: path, dir: path })) };
	});
	calls.on('project.variants', (p) => {
		const module = (p as { module: string }).module;
		return { modules: [{ module, variants: (o.variants ?? {})[module] ?? ['debug', 'release'], source: 'probe' }] };
	});
	calls.on('build.lastResult', () => {
		if (o.lastOutputs === undefined || o.lastOutputs === null) throw capError('BUILD_NOT_FOUND');
		return { buildId: 'b1', request: { tasks: [':app:assembleDebug'] }, outcome: 'success', outputs: o.lastOutputs };
	});
	calls.on('build.errors', (p) => ({ buildId: (p as { buildId: string }).buildId, outcome: 'success', errors: [] }));
	calls.on('device.list', () => ({ devices: o.devices ?? [] }));
	calls.on('emulator.list', () => ({ avds: o.avds ?? [] }));
	calls.on('emulator.start', () => ({ device: 'emulator-5554', avd: 'a1', adopted: false }));
	calls.on('operation.cancel', () => ({}));
	calls.on('device.install', () => ({ applicationId: 'com.x.app', apk: 'app-debug.apk' }));
	calls.on('file.read', async (p) => {
		expect((p as { path: string }).path).toBe(PROJECT_FILE);
		if (gate.read) await gate.read;
		fileRead();
		if (o.readError) throw o.readError;
		if (disk.content === null) throw capError('FILE_NOT_FOUND');
		const size = new TextEncoder().encode(disk.content).length;
		return { path: PROJECT_FILE, size, modTime: 't', offset: 0, nextOffset: size, text: disk.content };
	});
	calls.on('file.write', (p) => {
		if (o.writeError) throw o.writeError;
		const content = (p as { content: string }).content;
		disk.content = content;
		disk.writes.push(content);
		return { path: PROJECT_FILE, size: content.length };
	});

	const ws = new FakeWs();
	const build = new BuildModel(calls as unknown as BuildClient, act.store, hub, ws);
	const mirror = new DevicesMirror(act.store, calls as never);
	const devices = new DevicesModel(calls as unknown as DevicesClient, act.store);
	const emulators = new EmulatorsModel(calls as unknown as EmulatorsClient, act.store, mirror);
	const install = new InstallModel(
		calls as unknown as InstallClient,
		mirror,
		{
			selection: () => ({ root: ws.root, module: build.view.module, variant: build.view.variant }),
			subscribe: (l) => build.subscribe(l),
			output: (module, variant) => build.view.last?.outputs?.find((x) => x.module === module && x.variant === variant)?.applicationId,
		},
		devices,
	);
	const file = new ProjectFileModel(calls as unknown as ProjectFileClient, ws);
	bindProjectFile({ file, build, devices, emulators, install });
	await settle();
	await devices.refresh();
	await emulators.refresh();
	await settle();
	return { calls, disk, gate, ws, build, devices, emulators, install, file, written: () => disk.writes.map((w) => JSON.parse(w) as Record<string, unknown>) };
}

const fileOf = (o: Record<string, unknown>) => JSON.stringify(o);

describe('no project file (D19)', () => {
	// Decision (D19, ADR-0046 §2): defaults apply and opening leaves the tree untouched.
	// A file written on load would put .droidyard/ in every project that is merely opened.
	it('uses the defaults and writes nothing', async () => {
		const t = await setup({
			modules: [':app', ':lib'],
			devices: [dev('emulator-5554', { avdName: 'a1' }), dev('emulator-5556', { online: false, state: 'offline' })],
		});
		expect(t.build.view).toMatchObject({ module: ':app', variant: 'debug' });
		expect(t.devices.view.selected).toBe('emulator-5554');
		expect(t.file.view.state).toBe('absent');
		expect(t.calls.names()).not.toContain('file.write');
		expect(t.disk.content).toBeNull();
	});

	// Decision (D19): the defaults fall back as listed: `:app` and `debug` only if listed.
	it('takes the first real module and variant when :app and debug are not listed', async () => {
		const t = await setup({ modules: [':', ':feature'], variants: { ':feature': ['beta', 'prod'] } });
		expect(t.build.view).toMatchObject({ module: ':feature', variant: 'beta' });
	});

	// Decision (D19): the device default is the running emulator if exactly one; two
	// emulators choose nothing, and a physical device does not outrank the one emulator.
	it('selects no device when two emulators run, and the one emulator beside a physical device', async () => {
		const two = await setup({ devices: [dev('emulator-5554'), dev('emulator-5556')] });
		expect(two.devices.view.selected).toBe('');
		const mixed = await setup({ devices: [dev('R58M', { kind: 'physical' }), dev('emulator-5554')] });
		expect(mixed.devices.view.selected).toBe('emulator-5554');
	});
});

describe('the first change writes the file', () => {
	// Decision (ADR-0046 §1-§2): one change is one file.write of the whole file with
	// schemaVersion 1, the build pair together, and keys we do not know kept.
	it('writes schemaVersion 1 and the build pair once, keeping unknown keys', async () => {
		const existing = { schemaVersion: 1, custom: { x: 1 }, build: { module: ':app', variant: 'debug', extra: true }, other: [1, 2] };
		const t = await setup({ file: fileOf(existing) });
		expect(t.calls.names()).not.toContain('file.write');
		t.build.selectVariant('release');
		await settle();
		const writes = t.calls.calls.filter((c) => c.name === 'file.write');
		expect(writes).toHaveLength(1);
		expect(writes[0]!.workspace).toBe('/p');
		expect(t.written()[0]).toEqual({
			schemaVersion: 1,
			custom: { x: 1 },
			build: { module: ':app', variant: 'release', extra: true },
			other: [1, 2],
		});
	});

	// Decision (D19): with no file, the first change creates it with only what changed.
	it('creates the file with only the changed choice', async () => {
		const t = await setup({ modules: [':app', ':lib'] });
		await t.build.selectModule(':lib');
		await settle();
		expect(t.written()).toEqual([{ schemaVersion: 1, build: { module: ':lib', variant: 'debug' } }]);
		expect(t.disk.content?.endsWith('\n')).toBe(true);
	});

	// Decision (ADR-0046 §1): a device is stored as its AVD for an emulator and its serial
	// for a physical device; avd and serial exclude each other and a start counts as a choice.
	it('stores an emulator by AVD, a physical device by serial, and a started AVD', async () => {
		const t = await setup({
			devices: [dev('emulator-5554', { avdName: 'a1' }), dev('emulator-5556', { avdName: 'a2' }), dev('R58M', { kind: 'physical' })],
			avds: [{ name: 'a1' }, { name: 'a3' }],
		});
		t.devices.select('R58M');
		await settle();
		expect(t.written().at(-1)).toMatchObject({ device: { serial: 'R58M' } });
		t.devices.select('emulator-5556');
		await settle();
		expect(t.written().at(-1)?.device).toEqual({ avd: 'a2' });
		await t.emulators.start('a3');
		await settle();
		expect(t.written().at(-1)?.device).toEqual({ avd: 'a3' });
	});

	// Decision (ADR-0046 §2): the choice is not a default's echo: re-choosing what the file
	// already says writes nothing.
	it('does not write when the choice is what the file already holds', async () => {
		const t = await setup({ file: fileOf({ schemaVersion: 1, build: { module: ':app', variant: 'release' } }) });
		expect(t.build.view.variant).toBe('release');
		t.build.selectVariant('release');
		await settle();
		expect(t.calls.names()).not.toContain('file.write');
	});

	// Decision (ADR-0046 §2): last writer wins, but one write at a time: a change made
	// while a write is in flight is written after it, with both changes in it.
	it('writes changes made during a write after it, whole', async () => {
		const t = await setup({ modules: [':app'] });
		let release!: () => void;
		const held = new Promise<void>((r) => (release = r));
		const base = t.calls.handlers.get('file.write')!;
		t.calls.on('file.write', async (p) => {
			if (t.disk.writes.length === 0) await held;
			return base(p);
		});
		t.build.selectVariant('release');
		await settle();
		t.install.setApplicationId('com.x.typed'); // typed text is not a file change
		t.devices.select('');
		await t.emulators.start('a9'); // a second change while the first write waits
		release();
		await settle();
		expect(t.calls.calls.filter((c) => c.name === 'file.write')).toHaveLength(2);
		expect(t.written().at(-1)).toMatchObject({ build: { variant: 'release' }, device: { avd: 'a9' } });
	});
});

describe('the file only preselects', () => {
	const stored = fileOf({ schemaVersion: 1, build: { module: ':lib', variant: 'release' }, device: { avd: 'a2' } });

	// Decision (ADR-0046 §3): values the daemon's lists hold are preselected, whichever
	// arrives first, the file or the lists.
	it('applies the module, variant and running AVD the lists hold', async () => {
		const t = await setup({
			file: stored,
			modules: [':app', ':lib'],
			devices: [dev('emulator-5554', { avdName: 'a1' }), dev('emulator-5556', { avdName: 'a2' })],
		});
		expect(t.build.view).toMatchObject({ module: ':lib', variant: 'release' });
		expect(t.devices.view.selected).toBe('emulator-5556');
		expect(t.calls.names()).not.toContain('file.write');
	});

	it('applies them when the lists arrive after the file', async () => {
		const t = await setup({ file: stored, modules: [':app', ':lib'], modulesAfterFile: true });
		expect(t.build.view).toMatchObject({ module: ':lib', variant: 'release' });
	});

	it('applies them when the file arrives after the lists', async () => {
		const t = await setup({ file: null, modules: [':app', ':lib'] });
		expect(t.build.view.module).toBe(':app');
		t.build.setPreselect({ module: ':lib', variant: 'release' });
		await settle();
		expect(t.build.view).toMatchObject({ module: ':lib', variant: 'release' });
	});

	// Decision (ADR-0046 §3): a module, variant, AVD or serial the lists lack is ignored,
	// not applied and not an error; the defaults stand.
	it('ignores a module, variant, AVD and serial the daemon does not list', async () => {
		const t = await setup({
			file: fileOf({ schemaVersion: 1, build: { module: ':gone', variant: 'ghost' }, device: { avd: 'nope' } }),
			devices: [dev('emulator-5554', { avdName: 'a1' }), dev('emulator-5556', { avdName: 'a2' })],
			avds: [{ name: 'a1' }],
		});
		expect(t.build.view).toMatchObject({ module: ':app', variant: 'debug', loadError: null });
		expect(t.devices.view.selected).toBe('');
		expect(t.emulators.view.preselected).toBe('');
		const serial = await setup({ file: fileOf({ schemaVersion: 1, device: { serial: 'ZZ' } }), devices: [dev('emulator-5554')] });
		expect(serial.devices.view.selected).toBe('emulator-5554'); // only the default, not 'ZZ'
		const early = await setup({
			file: fileOf({ schemaVersion: 1, build: { module: ':gone', variant: 'ghost' } }),
			modulesAfterFile: true,
		});
		expect(early.build.view).toMatchObject({ module: ':app', variant: 'debug' });
		const variantOnly = await setup({ file: fileOf({ schemaVersion: 1, build: { module: ':app', variant: 'ghost' } }) });
		expect(variantOnly.build.view.variant).toBe('debug');
	});

	// Decision (m2c Q3, D19): an AVD that is named and not running preselects Start; a
	// running one is selected as a device instead.
	it('marks the named AVD that is not running for Start', async () => {
		const t = await setup({
			file: fileOf({ schemaVersion: 1, device: { avd: 'a2' } }),
			avds: [{ name: 'a1' }, { name: 'a2' }],
		});
		expect(t.emulators.view.preselected).toBe('a2');
		const running = await setup({
			file: fileOf({ schemaVersion: 1, device: { avd: 'a2' } }),
			avds: [{ name: 'a2', running: { device: 'emulator-5556' } }],
			devices: [dev('emulator-5556', { avdName: 'a2' })],
		});
		expect(running.emulators.view.preselected).toBe('');
		expect(running.devices.view.selected).toBe('emulator-5556');
	});

	// Decision (ADR-0046 §3): the user's own choice outranks the file, even when the file
	// arrives later; nothing the file holds replaces it.
	it('never replaces a choice the user made before the file was read', async () => {
		const t = await setup({ file: stored, modules: [':app', ':lib'] });
		const u = await setup({ file: null, modules: [':app', ':lib'] });
		u.build.selectVariant('release');
		u.devices.select('');
		await settle();
		u.build.setPreselect({ module: ':lib', variant: 'debug' });
		await settle();
		expect(u.build.view).toMatchObject({ module: ':app', variant: 'release' });
		expect(t.build.view.module).toBe(':lib');
	});

	// Decision (ADR-0046 §3): the file carries values, never keys: trust, policy, admission
	// or tool keys in it do nothing, and the UI makes no call because of them.
	it('does nothing with trust, policy, device or tool keys in the file', async () => {
		const t = await setup({
			file: fileOf({
				schemaVersion: 1,
				trust: { roots: ['/p'] },
				policy: { devices: { allowPhysical: true } },
				tools: { gradle: '/evil' },
				device: { allowPhysical: true, allowlist: ['*'] },
			}),
		});
		expect(t.calls.names().filter((n) => n.startsWith('policy.') || n === 'workspace.trust' || n === 'shell.run')).toEqual([]);
		expect(t.file.view.prefs).toEqual({});
	});

	// Decision (ADR-0046 §3, plan N8): a value of the wrong shape is ignored whole: text
	// with control characters, an oversized name, an application id that is not one.
	it('ignores values that are not names or application ids', async () => {
		const t = await setup({
			file: fileOf({
				schemaVersion: 1,
				build: { module: ':app\n', variant: 'x'.repeat(300) },
				device: { avd: 7 },
				applicationId: '"><img src=x onerror=alert(1)>',
			}),
		});
		expect(t.file.view.prefs).toEqual({});
		expect(t.install.view.applicationId).toBe('');
	});
});

describe('a file we must not overwrite', () => {
	const choose = async (t: Awaited<ReturnType<typeof setup>>) => {
		t.build.selectVariant('release');
		await t.emulators.start('a1');
		await settle();
	};

	// Decision (ADR-0046 §1): a newer schemaVersion is treated as absent, shown as such
	// and never overwritten; the choices last for the tab.
	it('never writes over a schemaVersion newer than ours, and says so', async () => {
		const newer = fileOf({ schemaVersion: 2, build: { module: ':lib', variant: 'release' }, future: true });
		const t = await setup({ file: newer, modules: [':app', ':lib'] });
		expect(t.file.view.state).toBe('newer');
		expect(t.file.view.notice).toContain('schemaVersion 2');
		expect(t.build.view).toMatchObject({ module: ':app', variant: 'debug' }); // treated as absent: not preselected
		await choose(t);
		expect(t.build.view.variant).toBe('release'); // the tab keeps the choice
		expect(t.calls.names()).not.toContain('file.write');
		expect(t.disk.content).toBe(newer);
	});

	// Decision (ADR-0046 §1): a file that does not parse is the user's text; replacing it
	// would destroy what they wrote.
	it('never writes over a file that does not parse, nor one that is not an object or has no version', async () => {
		for (const bad of ['{ "schemaVersion": 1,', '[1,2]', '{"build":{"module":":app"}}', '', '{"schemaVersion":0}', '{"schemaVersion":1.5}']) {
			const t = await setup({ file: bad });
			expect(t.file.view.state).toBe('invalid');
			expect(t.file.view.notice).toContain('could not be parsed');
			await choose(t);
			expect(t.calls.names()).not.toContain('file.write');
			expect(t.disk.content).toBe(bad);
		}
	});

	// Decision (ADR-0046 §1): a file that is not all read (a page cut, binary) is not
	// parsed from a fragment and not overwritten.
	it('does not overwrite a file whose read was cut short', async () => {
		const t2 = await setup({ file: fileOf({ schemaVersion: 1 }) });
		t2.calls.on('file.read', () => ({ path: PROJECT_FILE, size: 999999, modTime: 't', offset: 0, nextOffset: 100, text: '{"schemaVersion":1}' }));
		const model = new ProjectFileModel(t2.calls as unknown as ProjectFileClient, new FakeWs());
		await settle();
		model.update({ applicationId: 'com.x.app' });
		await settle();
		expect(model.view.state).toBe('invalid');
		expect(t2.calls.names()).not.toContain('file.write');
	});

	// Decision (ADR-0046 §1): a file the daemon refused to read is unknown, not absent.
	it('never writes when the read failed with a daemon error', async () => {
		const t = await setup({ readError: capError('FILE_IO_FAILED') });
		expect(t.file.view.state).toBe('unreadable');
		expect(t.file.view.notice).toContain('FILE_IO_FAILED');
		await choose(t);
		expect(t.calls.names()).not.toContain('file.write');
	});

	// Decision (ADR-0046 §2): a change made before the read answered is written after it
	// when the file may be written, and dropped when it may not.
	it('writes a change made during the read after it, and drops it for a file it must not touch', async () => {
		let open!: () => void;
		const first = await setup({ file: fileOf({ schemaVersion: 1, custom: 1 }) });
		first.gate.read = new Promise<void>((r) => (open = r));
		const model = new ProjectFileModel(first.calls as unknown as ProjectFileClient, Object.assign(new FakeWs(), { id: 'w9' }));
		model.update({ applicationId: 'com.x.app' });
		await settle();
		expect(first.calls.names().filter((n) => n === 'file.write')).toHaveLength(0);
		open();
		await settle();
		expect(first.written().at(-1)).toEqual({ schemaVersion: 1, custom: 1, applicationId: 'com.x.app' });

		const bad = await setup({ file: '{nope' });
		bad.gate.read = new Promise<void>((r) => (open = r));
		const m2 = new ProjectFileModel(bad.calls as unknown as ProjectFileClient, Object.assign(new FakeWs(), { id: 'w9' }));
		m2.update({ applicationId: 'com.x.app' });
		open();
		await settle();
		expect(bad.calls.names()).not.toContain('file.write');
		expect(bad.disk.content).toBe('{nope');
	});

	// Decision (ADR-0046 §1): a lost connection mid-read says nothing about the file: it is
	// read again when the socket reopens, not treated as absent (and so written over).
	it('reads again after a lost connection instead of treating the file as absent', async () => {
		const t = await setup({ file: fileOf({ schemaVersion: 1, build: { module: ':app', variant: 'release' } }) });
		const calls = t.calls;
		let fail = true;
		calls.on('file.read', () => {
			if (fail) throw new Error('connection lost');
			return { path: PROJECT_FILE, size: 20, modTime: 't', offset: 0, nextOffset: 20, text: '{"schemaVersion":1}' };
		});
		const ws = new FakeWs();
		const model = new ProjectFileModel(calls as unknown as ProjectFileClient, ws);
		await settle();
		expect(model.view.state).toBe('idle');
		model.update({ applicationId: 'com.x.app' });
		await settle();
		expect(calls.names()).not.toContain('file.write');
		fail = false;
		model.onConnection('open');
		await settle();
		expect(model.view.state).toBe('loaded');
		expect(t.written().at(-1)).toEqual({ schemaVersion: 1, applicationId: 'com.x.app' });
	});
});

describe('a write that fails', () => {
	// Decision (ADR-0046 §2): a read-only checkout keeps the choice for the tab and shows
	// the failure; the next change tries again.
	it('keeps the choice, shows the error, and tries again on the next change', async () => {
		const t = await setup({ writeError: capError('FILE_IO_FAILED', 'read-only file system') });
		t.build.selectVariant('release');
		await settle();
		expect(t.build.view.variant).toBe('release');
		expect(t.file.view.writeError).toMatchObject({ code: 'FILE_IO_FAILED' });
		t.calls.on('file.write', (p) => {
			const content = (p as { content: string }).content;
			t.disk.writes.push(content);
			return { path: PROJECT_FILE, size: content.length };
		});
		t.install.setApplicationId('x'); // not a file change
		await t.emulators.start('a1');
		await settle();
		expect(t.file.view.writeError).toBeNull();
		// The retry carries the earlier choice too: the whole file is written again.
		expect(t.written().at(-1)).toMatchObject({ build: { variant: 'release' }, device: { avd: 'a1' } });
	});
});

describe('the application id (D20)', () => {
	const out = (applicationId: string | undefined, over: Record<string, unknown> = {}) => ({ module: ':app', variant: 'debug', applicationId, ...over });

	// Decision (D20): the newest build's output for the chosen pair wins over the file's.
	it('shows the newest build output before the file\'s application id', async () => {
		const t = await setup({
			file: fileOf({ schemaVersion: 1, applicationId: 'com.old.app' }),
			lastOutputs: [out('com.new.app'), out('com.new.app.rel', { variant: 'release' })],
		});
		expect(t.install.view.applicationId).toBe('com.new.app');
		t.build.selectVariant('release');
		expect(t.install.view.applicationId).toBe('com.new.app.rel');
	});

	// Decision (D20): when the newest build has no output for the pair (build.run help
	// builds nothing installable), the file's application id is shown.
	it('falls back to the file\'s when the newest build has no output for the pair', async () => {
		const t = await setup({ file: fileOf({ schemaVersion: 1, applicationId: 'com.old.app' }), lastOutputs: [] });
		expect(t.install.view.applicationId).toBe('com.old.app');
		const other = await setup({
			file: fileOf({ schemaVersion: 1, applicationId: 'com.old.app' }),
			lastOutputs: [out('com.lib', { module: ':lib' }), out(undefined)],
		});
		expect(other.install.view.applicationId).toBe('com.old.app');
		const none = await setup({ file: fileOf({ schemaVersion: 1, applicationId: 'com.old.app' }) });
		expect(none.install.view.applicationId).toBe('com.old.app');
	});

	// Decision (D20, plan A10): a later tab shows the application id of the install an
	// earlier tab made: the install writes it, a new tab reads it, nothing is launched.
	it('writes the id an install reports, and a new tab shows it', async () => {
		const a = await setup({ devices: [dev('emulator-5554')] });
		await a.install.install();
		await settle();
		expect(a.written().at(-1)).toEqual({ schemaVersion: 1, applicationId: 'com.x.app' });
		const b = await setup({ file: a.disk.content });
		expect(b.install.view.applicationId).toBe('com.x.app');
		expect(b.calls.names()).not.toContain('device.launch');
		expect(b.calls.names()).not.toContain('file.write');
	});

	// Decision (D20): what the user typed wins until it is emptied; an install of this tab
	// is newer than the file's id.
	it('lets typing win, and an install of this tab outrank the file', async () => {
		const t = await setup({ file: fileOf({ schemaVersion: 1, applicationId: 'com.old.app' }), devices: [dev('emulator-5554')] });
		t.install.setApplicationId('com.typed.app');
		expect(t.install.view.applicationId).toBe('com.typed.app');
		t.install.setApplicationId('');
		expect(t.install.view.applicationId).toBe('com.old.app');
		await t.install.install();
		expect(t.install.view.applicationId).toBe('com.x.app');
		t.install.setStoredApplicationId('com.late.read'); // a file read that finished after
		expect(t.install.view.applicationId).toBe('com.x.app');
	});

	// Decision (ADR-0046 §3, plan N8): an id from the daemon that is not an application id
	// is not written into the file.
	it('does not write an install result that is not an application id', async () => {
		const t = await setup({ devices: [dev('emulator-5554')] });
		t.calls.on('device.install', () => ({ applicationId: 'not an id', apk: 'a.apk' }));
		await t.install.install();
		await settle();
		expect(t.calls.names()).not.toContain('file.write');
	});
});

describe('the parts', () => {
	// Decision (ADR-0046 §1): a patch replaces only the keys it names.
	it('keeps unknown keys beside the ones a patch replaces', () => {
		const raw = { schemaVersion: 1, a: 1, device: { serial: 'S', note: 'x' }, build: { module: ':a', variant: 'v', k: 2 } };
		expect(applyPatch(raw, { device: { avd: 'x' } })).toEqual({
			schemaVersion: 1,
			a: 1,
			device: { note: 'x', avd: 'x' },
			build: { module: ':a', variant: 'v', k: 2 },
		});
		expect(parseProjectFile('{"schemaVersion":1,"z":1}')).toEqual({ kind: 'ok', raw: { schemaVersion: 1, z: 1 } });
	});
});
