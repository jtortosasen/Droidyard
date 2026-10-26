import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import { SdkModel, inUseOf, type SdkClient } from './sdk';
import { PANELS } from '../ui/panels';
import { CapabilityError } from '../api/client';
import { activityWith, FakeCalls, settle, stateEvent } from '../build/testing';

const lic = { id: 'android-sdk-license', sha1: 'a'.repeat(40) };
const pkg = (id: string, over: Record<string, unknown> = {}) => ({
	id,
	version: '1.0',
	category: 'other',
	displayName: id,
	size: 1000,
	license: lic,
	licenseAccepted: false,
	dependencies: [],
	obsolete: false,
	...over,
});

interface Opts {
	found?: boolean;
	installed?: unknown[];
	available?: unknown[];
	licenseText?: string;
	targetRoot?: string;
}

async function setup(o: Opts = {}) {
	const act = await activityWith();
	const calls = new FakeCalls();
	const state = { available: o.available ?? [pkg('platform-tools', { category: 'platformTools', size: 5000 })], installed: o.installed ?? [] };
	calls.on('sdk.status', () => ({ found: o.found ?? true, root: o.found === false ? undefined : '/sdk', rejected: [], warnings: [], installed: { platforms: [], buildTools: [] } }));
	calls.on('sdk.packages', () => ({
		found: o.found ?? true,
		root: o.found === false ? undefined : '/sdk',
		installed: state.installed,
		available: state.available,
		truncated: false,
		licenses: [],
		repository: { state: 'read' },
		warnings: [],
	}));
	calls.on('sdk.licenses', () => ({
		root: o.targetRoot ?? '/home/u/Android/Sdk',
		licenses: [{ ...lic, accepted: false, text: o.licenseText ?? 'Terms', packages: [] }],
	}));
	calls.on('sdk.acceptLicense', () => ({ root: '/sdk', ...lic, added: true }));
	calls.on('sdk.install', () => ({ root: '/sdk', created: false, installed: [], updated: [], downloaded: 0, warnings: [] }));
	calls.on('sdk.uninstall', () => ({ removed: [], freed: 0, warnings: [] }));
	calls.on('operation.cancel', () => ({}));
	const sdk = new SdkModel(calls as unknown as SdkClient, act.store);
	await sdk.refresh();
	calls.calls.length = 0;
	return { ...act, calls, sdk, state };
}

describe('the install plan', () => {
	// Decision (step 8): the plan sums the sizes of the selection and of the dependencies it
	// pulls in, and marks an update as one.
	it('sums sizes and marks updates', async () => {
		const t = await setup({
			installed: [{ id: 'emulator', version: '1', update: '2' }],
			available: [pkg('emulator', { size: 700, licenseAccepted: true }), pkg('platform-tools', { size: 300, licenseAccepted: true })],
		});
		t.sdk.toggle('emulator');
		t.sdk.toggle('platform-tools');
		const p = t.sdk.view.plan;
		expect(p.totalSize).toBe(1000);
		expect(p.items.find((i) => i.id === 'emulator')?.update).toBe(true);
		expect(p.items.find((i) => i.id === 'platform-tools')?.update).toBe(false);
	});

	// Decision (ADR-0015 §6): a dependency's unaccepted licence is among the pending ones, and
	// its size is in the total. Ignoring dependencies would hide a licence the daemon refuses on.
	it('lists the pending licence of a dependency and counts its size', async () => {
		const other = { id: 'other-license', sha1: 'b'.repeat(40) };
		const t = await setup({
			available: [
				pkg('system-images;x', { size: 100, licenseAccepted: true, dependencies: [{ id: 'emulator' }] }),
				pkg('emulator', { size: 400, license: other, licenseAccepted: false }),
			],
		});
		t.sdk.toggle('system-images;x');
		const p = t.sdk.view.plan;
		expect(p.items.map((i) => i.id)).toEqual(['system-images;x', 'emulator']);
		expect(p.items[1]?.dependency).toBe(true);
		expect(p.totalSize).toBe(500);
		expect(p.pending.map((l) => l.id)).toEqual(['other-license']);
		expect(p.pending[0]?.packages).toEqual(['emulator']);
	});

	// Decision (step 8): the Install action stays disabled while a licence is pending, and
	// Install without Accept calls nothing; Accept sends that licence's own hash.
	it('keeps Install disabled until every pending licence is accepted', async () => {
		const t = await setup();
		t.sdk.toggle('platform-tools');
		expect(t.sdk.view.plan.pending).toHaveLength(1);
		expect(t.sdk.view.canRun).toBe(false);
		await t.sdk.run();
		expect(t.calls.names()).not.toContain('sdk.install');
		t.state.available = [pkg('platform-tools', { category: 'platformTools', size: 5000, licenseAccepted: true })];
		await t.sdk.accept(lic.id, lic.sha1);
		expect(t.calls.calls.find((c) => c.name === 'sdk.acceptLicense')?.params).toEqual({ id: lic.id, sha1: lic.sha1 });
		expect(t.sdk.view.canRun).toBe(true);
		await t.sdk.run();
		expect(t.calls.calls.find((c) => c.name === 'sdk.install')?.params).toEqual({ packages: ['platform-tools'] });
	});
});

describe('untrusted text', () => {
	// Decision (step 8, ADR-0129): a licence text is data. The model keeps it verbatim and the
	// component shows it only through a text interpolation; web/sinks_test.go refuses every
	// HTML sink in the UI source. There is no DOM in this test environment, so the
	// interpolation is checked on the component's source.
	it('carries a licence holding <b> verbatim and renders it as a text node', async () => {
		const t = await setup({ licenseText: 'a <b>bold</b> <img src=x onerror=alert(1)>' });
		t.sdk.toggle('platform-tools');
		expect(t.sdk.view.plan.pending[0]?.text).toBe('a <b>bold</b> <img src=x onerror=alert(1)>');
		const src = new TextDecoder().decode(readFileSync(new URL('./Sdk.svelte', import.meta.url)));
		expect(src).toContain('<pre class="text">{l.text}</pre>');
	});

	// Decision (ADR-0129 §8): SDK_PACKAGE_IN_USE names each package and what uses it.
	it('reads each package and its users from SDK_PACKAGE_IN_USE', async () => {
		const t = await setup({ installed: [{ id: 'platform-tools', version: '1' }] });
		t.calls.on('sdk.uninstall', () => {
			throw new CapabilityError(
				{
					code: 'SDK_PACKAGE_IN_USE',
					message: 'in use',
					details: { packages: [{ id: 'platform-tools', usedBy: ['adb (pid 4)', 'emulator-5554'] }, { id: 'emulator', usedBy: [] }] },
				},
				true,
			);
		});
		t.sdk.openUninstall('platform-tools');
		await t.sdk.uninstall();
		expect(t.sdk.view.error?.code).toBe('SDK_PACKAGE_IN_USE');
		expect(t.sdk.view.inUse).toEqual([
			{ id: 'platform-tools', usedBy: ['adb (pid 4)', 'emulator-5554'] },
			{ id: 'emulator', usedBy: [] },
		]);
		expect(inUseOf({ packages: 'x' })).toEqual([]);
	});
});

describe('refreshes and the no-SDK state', () => {
	// Decision (step 8): an agent's sdk.install seen on the bus reads the lists again; a read is no trigger.
	it('reads again when any client\'s sdk.install ends, and not for a read', async () => {
		const t = await setup();
		stateEvent(t.watch, 'op-1', { capability: 'sdk.packages', state: 'completed' }, null);
		await settle();
		expect(t.calls.names()).toEqual([]);
		stateEvent(t.watch, 'op-2', { capability: 'sdk.install', state: 'completed', client: 'mcp' }, null);
		await settle();
		expect(t.calls.names().sort()).toEqual(['sdk.licenses', 'sdk.packages', 'sdk.status']);
	});

	// Decision (step 8): with no SDK the panel shows the root a setup would use, and Set up
	// plans platform-tools alone.
	it('shows the target root with no SDK and plans platform-tools on Set up', async () => {
		const t = await setup({ found: false, targetRoot: '/home/u/Android/Sdk' });
		expect(t.sdk.view.found).toBe(false);
		expect(t.sdk.view.targetRoot).toBe('/home/u/Android/Sdk');
		t.sdk.setUp();
		expect(t.sdk.view.confirming).toBe(true);
		expect(t.sdk.view.plan.items.map((i) => i.id)).toEqual(['platform-tools']);
	});

	// Decision (step 8): an install by another client shows as the running operation with its
	// client kind, and blocks this tab's Install.
	it('shows another client\'s install and blocks Install', async () => {
		const t = await setup();
		t.sdk.toggle('platform-tools');
		stateEvent(t.watch, 'op-3', { capability: 'sdk.install', state: 'running', client: 'mcp' }, null);
		expect(t.sdk.view.op).toMatchObject({ operationId: 'op-3', client: 'mcp' });
		expect(t.sdk.view.canRun).toBe(false);
		await t.sdk.cancel('op-3');
		expect(t.calls.calls.at(-1)).toMatchObject({ name: 'operation.cancel', params: { operationId: 'op-3' } });
	});

	it('registers the SDK as a centre panel', () => {
		expect(PANELS.find((p) => p.id === 'sdk')).toMatchObject({ region: 'centre', label: 'SDK' });
	});
});

describe('the category filter', () => {
	// Decision (step 9): New AVD opens the panel on system images; Show all clears it.
	it('narrows the groups to one category and clears', async () => {
		const t = await setup({
			available: [pkg('platform-tools', { category: 'platformTools' }), pkg('system-images;android-35;x;arm64-v8a', { category: 'systemImage' })],
		});
		t.sdk.setCategory('systemImage');
		expect(t.sdk.view.groups.map((g) => g.category)).toEqual(['systemImage']);
		expect(t.sdk.view.category).toBe('systemImage');
		t.sdk.setCategory(null);
		expect(t.sdk.view.groups.length).toBe(2);
	});
});
