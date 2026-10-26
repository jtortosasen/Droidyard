import { describe, expect, it } from 'vitest';
import { CapabilityError } from '../api/client';
import { EmulatorsModel, rowActions, type EmulatorsClient } from './emulators';
import { changedSettings, draftOf, draftProblems, emptyDraft, nameProblem, type Settings } from './avdform';
import { DevicesMirror } from './mirror';
import { activityWith, FakeCalls, settle, stateEvent } from '../build/testing';

const stored: Settings = { displayName: 'Pixel', ramMb: 2048, internalStorageMb: 8192, cpuCores: 4, graphics: 'automatic', deviceFrame: true };

async function setup(avds: unknown[] = [{ name: 'a1', config: { settings: stored } }, { name: 'live', running: { device: 'emulator-5554' }, config: { settings: stored } }]) {
	const act = await activityWith();
	const calls = new FakeCalls();
	let list = avds;
	calls.on('emulator.list', () => ({ avds: list }));
	calls.on('emulator.create', (p) => {
		list = [...list, { name: (p as { avd: string }).avd }];
		return { avd: (p as { avd: string }).avd, profile: 'pixel_9a', systemImage: 'img' };
	});
	calls.on('emulator.configure', () => ({ avd: 'a1', settings: stored }));
	calls.on('emulator.delete', () => ({ avd: 'a1' }));
	calls.on('emulator.wipe', () => ({ device: 'emulator-5554', avd: 'a1' }));
	calls.on('sdk.profiles', () => ({ profiles: [{ id: 'pixel_9a', name: 'Pixel 9a', oem: 'Google' }] }));
	calls.on('sdk.status', () => ({ installed: { systemImages: [{ id: 'system-images;android-35;google_apis;arm64-v8a' }] } }));
	const mirror = new DevicesMirror(act.store, calls as never);
	const emulators = new EmulatorsModel(calls as unknown as EmulatorsClient, act.store, mirror);
	await emulators.refresh();
	await settle();
	calls.calls.length = 0;
	return { ...act, calls, emulators, setList: (l: unknown[]) => (list = l) };
}

const input = (over: Partial<{ name: string; profile: string; image: string }> = {}, draft = emptyDraft()) => ({
	name: 'new1',
	profile: 'pixel_9a',
	image: 'img',
	draft,
	...over,
});

describe('New AVD', () => {
	// Decision (Q9): a name already listed is refused before emulator.create is called.
	it('refuses a listed name without calling', async () => {
		const t = await setup();
		const problems = await t.emulators.create(input({ name: 'A1' }));
		expect(problems.avd).toContain('already listed');
		expect(t.calls.names()).not.toContain('emulator.create');
		expect(nameProblem('a b', [])).toContain('letters, digits');
	});

	// Decision (Q9): the form checks each range and shows it; nothing out of range is sent.
	it('refuses an out-of-range RAM in the form and shows the range', async () => {
		const t = await setup();
		const draft = { ...emptyDraft(), ramMb: '100' };
		const problems = await t.emulators.create(input({}, draft));
		expect(problems.ramMb).toContain('512 to 16384');
		expect(t.calls.names()).not.toContain('emulator.create');
		expect(draftProblems({ ...emptyDraft(), ramMb: '600', vmHeapMb: '400' }).vmHeapMb).toContain('half of RAM');
		expect(draftProblems({ ...emptyDraft(), sdcardMb: '0' }).sdcardMb).toBeUndefined();
		expect(draftProblems({ ...emptyDraft(), sdcardMb: '10' }).sdcardMb).toContain('32 to 65536');
	});

	// Decision (Q9): empty fields are the profile's and image's default: only the filled ones are sent.
	it('sends only the filled settings and refreshes the list', async () => {
		const t = await setup();
		await t.emulators.create(input({}, { ...emptyDraft(), ramMb: '4096', keyboard: 'false' }));
		expect(t.calls.calls.find((c) => c.name === 'emulator.create')?.params).toEqual({
			avd: 'new1',
			profile: 'pixel_9a',
			systemImage: 'img',
			settings: { ramMb: 4096, keyboard: false },
		});
		expect(t.emulators.view.avds.map((a) => a.name)).toContain('new1');
		await t.emulators.create(input({ name: 'plain' }));
		expect(t.calls.calls.filter((c) => c.name === 'emulator.create').at(-1)?.params).not.toHaveProperty('settings');
	});

	// Decision (Q9): with no installed image the form offers the SDK panel; the code that says so is kept.
	it('reads profiles and installed images, and keeps EMULATOR_IMAGE_NOT_INSTALLED for the SDK link', async () => {
		const t = await setup();
		await t.emulators.loadChoices();
		expect(t.emulators.view.profiles.map((p) => p.id)).toEqual(['pixel_9a']);
		expect(t.emulators.view.images).toEqual(['system-images;android-35;google_apis;arm64-v8a']);
		t.calls.on('emulator.create', () => {
			throw new CapabilityError({ code: 'EMULATOR_IMAGE_NOT_INSTALLED', message: 'no image' }, false);
		});
		await t.emulators.create(input());
		expect(t.emulators.view.createError?.code).toBe('EMULATOR_IMAGE_NOT_INSTALLED');
	});

	// Decision (D13): an agent's emulator.create ends as a terminal operation and the list is read again.
	it('refreshes the list when any client creates an AVD', async () => {
		const t = await setup();
		t.setList([{ name: 'a1' }, { name: 'by-agent' }]);
		stateEvent(t.watch, 'op-c', { capability: 'emulator.create', state: 'running' }, null);
		stateEvent(t.watch, 'op-c', { capability: 'emulator.create', state: 'succeeded' }, null);
		await settle();
		expect(t.emulators.view.avds.map((a) => a.name)).toContain('by-agent');
	});
});

describe('Edit, Wipe and Delete', () => {
	// Decision (Q9): Edit sends only what changed; the SD card is never sent.
	it('sends only the changed fields', async () => {
		const t = await setup();
		const draft = { ...draftOf(stored), ramMb: '4096', sdcardMb: '64' };
		await t.emulators.configure('a1', draft);
		expect(t.calls.calls.find((c) => c.name === 'emulator.configure')?.params).toEqual({ avd: 'a1', settings: { ramMb: 4096 } });
		t.calls.calls.length = 0;
		await t.emulators.configure('a1', draftOf(stored));
		expect(t.calls.names()).not.toContain('emulator.configure');
		expect(changedSettings(draftOf(stored), stored)).toEqual({});
	});

	// Decision (Q9): internal storage is never below the current value; the form says so.
	it('refuses internal storage below the current one', async () => {
		const t = await setup();
		const problems = await t.emulators.configure('a1', { ...draftOf(stored), internalStorageMb: '4096' });
		expect(problems.internalStorageMb).toContain('not below the current 8192');
		expect(t.calls.names()).not.toContain('emulator.configure');
	});

	// Decision (Q9): Edit and Delete are disabled on a running row with the reason, and the model makes no call either.
	it('disables Edit and Delete on a running AVD', async () => {
		const t = await setup();
		const live = t.emulators.view.avds.find((a) => a.name === 'live')!;
		expect(rowActions(live, false)).toMatchObject({ edit: false, remove: false, why: 'Edit and Delete need live stopped.' });
		expect(rowActions(t.emulators.view.avds[0]!, false)).toMatchObject({ edit: true, remove: true, why: null });
		await t.emulators.configure('live', { ...draftOf(stored), ramMb: '4096' });
		await t.emulators.remove('live');
		expect(t.calls.names()).not.toContain('emulator.configure');
		expect(t.calls.names()).not.toContain('emulator.delete');
	});

	it('deletes a stopped AVD and wipes through emulator.wipe', async () => {
		const t = await setup();
		await t.emulators.remove('a1');
		expect(t.calls.calls.find((c) => c.name === 'emulator.delete')?.params).toEqual({ avd: 'a1' });
		await t.emulators.wipe('a1');
		expect(t.calls.calls.find((c) => c.name === 'emulator.wipe')?.params).toEqual({ avd: 'a1' });
	});
});
