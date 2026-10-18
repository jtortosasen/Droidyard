import { describe, expect, it } from 'vitest';
import type { Params } from '../api/capabilities.gen';
import { CapabilityError } from '../api/client';
import { activityWith, FakeCalls, settle, stateEvent } from '../build/testing';
import { ActModel, buildExpect, buildTarget, MAX_EXPECT, summarize, type ActClient, type ExpectRow } from './act';
import { AppLogsModel, crashView, logLine, type AppLogsClient } from './applogs';
import { inert } from './text';
import { selectorFor, treeRows, UiTreeModel, type UiNode, type UiTreeClient } from './uitree';

class Selection {
	view = { selected: 'emulator-5554' };
	#l = new Set<() => void>();
	subscribe(l: () => void) {
		this.#l.add(l);
		return () => this.#l.delete(l);
	}
	select(s: string) {
		this.view = { selected: s };
		for (const l of [...this.#l]) l();
	}
}

const node = (ref: string, over: Partial<UiNode> = {}): UiNode => ({ ref, bounds: [0, 0, 1, 1], ...over });

const tree = (nodes: UiNode[], over: Record<string, unknown> = {}) => ({
	snapshot: 's1',
	foreground: 'com.x.app',
	display: { width: 1080, height: 2400, rotation: 0 },
	nodes,
	...over,
});

const logs = (over: Record<string, unknown> = {}) => ({
	device: 'emulator-5554',
	window: { from: '2026-10-10T10:00:00Z', to: '2026-10-10T10:00:05Z' },
	filter: { applicationId: 'com.x.app', by: 'uid' },
	lines: [{ time: 't1', pid: 7, tid: 7, priority: 'info', tag: 'GoldenApp', message: 'started' }],
	crashes: [],
	counts: { scanned: 10, matched: 1, returned: 1 },
	truncated: false,
	...over,
});

async function setup() {
	const act = await activityWith();
	const calls = new FakeCalls();
	const sel = new Selection();
	const app = { view: { applicationId: 'com.x.app' }, subscribe: () => () => {} };
	calls.on('device.uiTree', () => tree([node('n1', { id: 'name' })]));
	calls.on('device.logs', () => logs());
	calls.on('device.tapElement', () => ({ tapped: { ref: 'n1', snapshot: 's1', matchedBy: 'exact', point: [1, 2], clickable: true } }));
	calls.on('device.inputText', () => ({ typed: true, characters: 2, method: 'set_text' }));
	const treeModel = new UiTreeModel(calls as unknown as UiTreeClient, sel, act.store);
	const actModel = new ActModel(calls as unknown as ActClient, sel);
	const logModel = new AppLogsModel(calls as unknown as AppLogsClient, sel, app, act.store);
	await settle();
	return { ...act, calls, sel, app, treeModel, actModel, logModel };
}

let seq = 0;
// Each ended operation is a new one: a terminal state is never replaced, so an id is used once.
const done = (t: Awaited<ReturnType<typeof setup>>, capability: string, id = `op-${++seq}`, target?: Record<string, string>) => {
	stateEvent(t.watch, id, { capability, state: 'completed', client: 'mcp', target }, null);
	return settle();
};

describe('the selector a picked node gives', () => {
	// Decision (step 6): id when the node has one, else text, else {ref, snapshot}.
	it('prefers the id, then the text, then the ref with its snapshot', () => {
		expect(selectorFor(node('n1', { id: 'name', text: 'Name' }), 's1')).toEqual({ id: 'name' });
		expect(selectorFor(node('n2', { text: 'Submit', desc: 'send' }), 's1')).toEqual({ text: 'Submit' });
		expect(selectorFor(node('n3', { desc: 'only a description' }), 's7')).toEqual({ ref: 'n3', snapshot: 's7' });
		expect(selectorFor(node('n4', { id: '', text: '' }), 's7')).toEqual({ ref: 'n4', snapshot: 's7' });
	});

	// Decision: the selector holds the device's exact text, not the escaped display form.
	it('keeps the raw text for the selector while the row shows it escaped', () => {
		const n = node('n1', { text: 'a‮b' });
		expect(selectorFor(n, 's')).toEqual({ text: 'a‮b' });
		expect(treeRows([n])[0]?.text).toBe('a\\u{202e}b');
	});
});

describe('the tree rows', () => {
	// Decision: nodes are indented by `children`; a cycle or a repeated ref is shown once.
	it('indents by children and shows each node once', () => {
		const rows = treeRows([
			node('r', { children: ['a', 'b'] }),
			node('a', { children: ['c'] }),
			node('c'),
			node('b', { children: ['r'] }),
		]);
		expect(rows.map((r) => [r.index, r.indent.length])).toEqual([
			[0, 0],
			[1, 2],
			[2, 4],
			[3, 2],
		]);
		expect(treeRows([node('x'), node('x')]).length).toBe(2);
	});
});

describe('the UI tree model', () => {
	// Decision (step 6 and plan §Changes): a tap, typed text, launch or install by any
	// client re-reads a shown tree; nothing shown, nothing read.
	it('re-reads a shown tree when an action ends, by any client, and not before it is shown', async () => {
		const t = await setup();
		for (const c of ['device.tapElement', 'device.inputText', 'device.launch', 'device.install']) await done(t, c);
		expect(t.calls.names()).toEqual([]);
		await t.treeModel.load();
		t.calls.calls.length = 0;
		for (const c of ['device.tapElement', 'device.inputText', 'device.launch', 'device.install']) {
			await done(t, c);
			expect(t.calls.names().filter((n) => n === 'device.uiTree'), c).toHaveLength(1);
			t.calls.calls.length = 0;
		}
	});

	// Decision (ADR-0076, closing R2): an operation that names another device does not
	// re-read this device's tree; one on this device, or naming none, does.
	it('re-reads only for an operation on its own device or on none', async () => {
		const t = await setup();
		await t.treeModel.load();
		t.calls.calls.length = 0;
		await done(t, 'device.tapElement', undefined, { device: 'emulator-5556' });
		await done(t, 'device.install', undefined, { device: 'emulator-5556', module: ':app', variant: 'debug' });
		expect(t.calls.names()).toEqual([]);
		await done(t, 'device.tapElement', undefined, { device: 'emulator-5554' });
		expect(t.calls.names().filter((n) => n === 'device.uiTree')).toHaveLength(1);
		t.calls.calls.length = 0;
		await done(t, 'device.launch');
		expect(t.calls.names().filter((n) => n === 'device.uiTree')).toHaveLength(1);
	});

	// Decision (plan m2p step 7): the live view's gestures and keys re-read a shown tree, but
	// not App logs, which keep UI_TRIGGERS: a gesture does not cost a device.logs read.
	it("re-reads the tree for the view's input and the logs do not", async () => {
		const t = await setup();
		await t.treeModel.load();
		await t.logModel.load();
		for (const c of ['device.tap', 'device.swipe', 'device.pressKey']) {
			t.calls.calls.length = 0;
			await done(t, c, undefined, { device: 'emulator-5554' });
			expect(t.calls.names().filter((n) => n === 'device.uiTree'), c).toHaveLength(1);
			expect(t.calls.names().filter((n) => n === 'device.logs'), c).toHaveLength(0);
		}
	});

	// Decision (D13): a read is never its own trigger; unrelated operations cause none.
	it('does not re-read when a tree read or a list ends', async () => {
		const t = await setup();
		await t.treeModel.load();
		t.calls.calls.length = 0;
		for (const c of ['device.uiTree', 'device.logs', 'device.list', 'build.assemble']) await done(t, c);
		expect(t.calls.names()).toEqual([]);
	});

	// Decision: a tree belongs to the device it was read from.
	it('forgets the tree when another device is selected', async () => {
		const t = await setup();
		await t.treeModel.load();
		expect(t.treeModel.view.rows).toHaveLength(1);
		t.sel.select('emulator-5556');
		expect(t.treeModel.view.rows).toHaveLength(0);
		expect(t.treeModel.view.loaded).toBe(false);
		t.calls.calls.length = 0;
		await done(t, 'device.tapElement');
		expect(t.calls.names()).toEqual([]);
	});

	// Decision: a newer read supersedes an older one on its way.
	it('shows the newest read when two overlap', async () => {
		const t = await setup();
		const gates: ((v: unknown) => void)[] = [];
		t.calls.on('device.uiTree', () => new Promise((r) => gates.push(r)));
		const a = t.treeModel.load();
		const b = t.treeModel.load();
		gates[1]!(tree([node('new', { id: 'new' })]));
		await b;
		gates[0]!(tree([node('old', { id: 'old' })]));
		await a;
		expect(t.treeModel.view.rows.map((r) => r.id)).toEqual(['new']);
	});

	// Decision: a capability error is shown by its code, as text.
	it('shows a refused read by its code', async () => {
		const t = await setup();
		t.calls.on('device.uiTree', () => {
			throw new CapabilityError({ code: 'DEVICE_UI_DUMP_FAILED', message: 'no dump' }, true);
		});
		await t.treeModel.load();
		expect(t.treeModel.view.error?.code).toBe('DEVICE_UI_DUMP_FAILED');
	});
});

describe('expectations and the action params', () => {
	const row = (over: Partial<ExpectRow> = {}): ExpectRow => ({ kind: 'appears', by: 'text', value: 'Hi', ...over });

	// Decision (schema): at most 5 expectations, appears or gone, by id or text; the
	// result must type-check as the generated Params (svelte-check proves the assignment).
	it('builds appears and gone by id or text, at most five', () => {
		const p: Params<'device.tapElement'> = {
			device: 'd',
			target: { id: 'x' },
			expect: buildExpect([row(), row({ kind: 'gone', by: 'id', value: 'spin' })]),
		};
		expect(p.expect).toEqual([{ appears: { text: 'Hi' } }, { gone: { id: 'spin' } }]);
		const q: Params<'device.inputText'> = { device: 'd', text: 't', expect: buildExpect(Array.from({ length: 9 }, () => row())) };
		expect(q.expect).toHaveLength(MAX_EXPECT);
		expect(buildExpect([row({ value: '' })])).toBeUndefined();
	});

	// Decision (schema): a ref stands alone beside its snapshot; typed fields only when set.
	it('builds a target from a ref alone or from the typed fields', () => {
		expect(buildTarget({ id: 'a', text: 'b', desc: '', cls: '' }, { ref: 'r', snapshot: 's' })).toEqual({ ref: 'r', snapshot: 's' });
		expect(buildTarget({ id: 'a', text: '', desc: 'd', cls: 'Button' }, null)).toEqual({ id: 'a', desc: 'd', class: 'Button' });
		expect(buildTarget({ id: '', text: '', desc: '', cls: '' }, null)).toBeNull();
	});
});

describe('the action model', () => {
	// Decision (G7, same-primitive check): the panel calls device.tapElement with the
	// picked selector, the expectations and the timeout, on the selected device.
	it('taps the picked node with its expectations', async () => {
		const t = await setup();
		t.actModel.pick({ id: 'submit' });
		t.actModel.addExpect();
		t.actModel.setExpect(0, { kind: 'gone', by: 'id', value: 'spinner' });
		t.actModel.setTimeoutText('3000');
		await t.actModel.tap();
		expect(t.calls.calls.at(-1)).toMatchObject({
			name: 'device.tapElement',
			params: { device: 'emulator-5554', target: { id: 'submit' }, expect: [{ gone: { id: 'spinner' } }], timeoutMs: 3000 },
		});
	});

	// Decision (step 6 review): a picked node and a result belong to their device; another
	// selection drops them, so a ref from one device is never tapped on another.
	it('drops a picked node and the last result when another device is selected', async () => {
		const t = await setup();
		t.actModel.pick({ ref: 'n9', snapshot: 's4' });
		await t.actModel.tap();
		expect(t.actModel.view.result).not.toBeNull();
		t.sel.select('emulator-5556');
		expect(t.actModel.view.picked).toBeNull();
		expect(t.actModel.view.result).toBeNull();
		t.sel.select('emulator-5556');
		expect(t.actModel.view.device).toBe('emulator-5556');
	});

	// Decision: the forms the schema allows: a picked ref stands alone, and typing replaces it.
	it('sends a picked ref with its snapshot, and a typed field replaces it', async () => {
		const t = await setup();
		t.actModel.pick({ ref: 'n9', snapshot: 's4' });
		await t.actModel.tap();
		expect((t.calls.calls.at(-1)?.params as { target: unknown }).target).toEqual({ ref: 'n9', snapshot: 's4' });
		t.actModel.setField('text', 'Go');
		await t.actModel.tap();
		expect((t.calls.calls.at(-1)?.params as { target: unknown }).target).toEqual({ text: 'Go' });
	});

	// Decision (G7): device.inputText takes the target and text; clear and submit only when set.
	it('types into the target with clear and submit', async () => {
		const t = await setup();
		t.actModel.pick({ id: 'name' });
		t.actModel.setTypeText('Ana');
		t.actModel.setClear(true);
		await t.actModel.type();
		expect(t.calls.calls.at(-1)).toMatchObject({
			name: 'device.inputText',
			params: { device: 'emulator-5554', target: { id: 'name' }, text: 'Ana', clear: true },
		});
		expect(t.calls.calls.at(-1)?.params).not.toHaveProperty('submit');
	});

	// Decision: nothing is sent that the schema or the form forbids; the reason is shown.
	it('refuses without a device, a target, a valid timeout or a target to clear', async () => {
		const t = await setup();
		await t.actModel.tap();
		expect(t.actModel.view.invalid).toMatch(/Pick a node/);
		t.actModel.pick({ id: 'x' });
		t.actModel.setTimeoutText('99999');
		await t.actModel.tap();
		expect(t.actModel.view.invalid).toMatch(/timeoutMs/);
		t.actModel.setTimeoutText('');
		t.actModel.setField('id', '');
		t.actModel.setClear(true);
		await t.actModel.type();
		expect(t.actModel.view.invalid).toMatch(/Clear needs a target/);
		t.sel.select('');
		await t.actModel.tap();
		expect(t.actModel.view.invalid).toMatch(/Select a device/);
		expect(t.calls.names()).toEqual([]);
	});

	// Decision: an action is never repeated or retried: a refused one is shown, one call only.
	it('shows a refused action once, with its details as text, and does not retry', async () => {
		const t = await setup();
		t.calls.on('device.tapElement', () => {
			throw new CapabilityError({ code: 'DEVICE_ELEMENT_AMBIGUOUS', message: 'two <b>nodes</b>', details: { candidates: ['<img src=x>'] } }, true);
		});
		t.actModel.pick({ text: 'Go' });
		await t.actModel.tap();
		expect(t.calls.names()).toEqual(['device.tapElement']);
		expect(t.actModel.view.error).toMatchObject({ code: 'DEVICE_ELEMENT_AMBIGUOUS', message: 'two <b>nodes</b>' });
		expect(t.actModel.view.error?.details).toContain('<img src=x>');
	});

	// Decision: a second click while one runs sends nothing.
	it('ignores a second action while one runs', async () => {
		const t = await setup();
		let release: (v: unknown) => void = () => {};
		t.calls.on('device.tapElement', () => new Promise((r) => (release = r)));
		t.actModel.pick({ id: 'x' });
		const first = t.actModel.tap();
		await t.actModel.tap();
		release({});
		await first;
		expect(t.calls.names().filter((n) => n === 'device.tapElement')).toHaveLength(1);
	});
});

describe('the answer summary', () => {
	const cond = { appears: { text: 'Done' } };

	// Decision (device.tapElement): matched is a verdict, and a completed call.
	it('reports matched with its time', () => {
		const s = summarize('tap', { expect: { matched: true, afterMs: 120, polls: 2, failedDumps: 0 } });
		expect(s).toMatchObject({ matched: true, headline: 'Matched after 120 ms' });
	});

	// Decision: unmet conditions carry candidates; vacuous ones are marked as proving nothing.
	it('reports unmet conditions with candidates, and vacuous ones', () => {
		const s = summarize('tap', {
			expect: {
				matched: false,
				afterMs: 5000,
				polls: 9,
				failedDumps: 1,
				vacuous: [{ gone: { id: 'spinner' } }],
				unmet: [{ condition: cond, candidates: [{ class: 'Button', text: 'Doing' }] }],
			},
		});
		expect(s.matched).toBe(false);
		expect(s.headline).toBe('Not matched after 5000 ms');
		expect(s.lines).toContain('Unmet: appears: text "Done"');
		expect(s.lines).toContain('  candidate: "Button" "Doing"');
		expect(s.lines.some((l) => l.startsWith('Already true before the action') && l.includes('gone: id "spinner"'))).toBe(true);
	});

	// Decision: an appCrash is shown whole, with the system dialog, as text.
	it('reports an app crash with its frames and a system dialog', () => {
		const s = summarize('tap', {
			appCrash: { package: 'com.x.app', pid: 7, exception: 'java.lang.IllegalStateException', message: 'golden app crash', frames: ['com.x.A.b(A.kt:1)'], source: 'logcat' },
			systemDialog: { kind: 'crash', package: 'com.x.app' },
			warnings: ['w'],
		});
		expect(s.matched).toBeNull();
		expect(s.lines).toContain('java.lang.IllegalStateException: golden app crash');
		expect(s.lines).toContain('  at com.x.A.b(A.kt:1)');
		expect(s.lines).toContain('System dialog (crash) of "com.x.app".');
		expect(s.lines).toContain('Warning: w');
	});

	// Decision: device text is shown with control and bidi characters as code points.
	it('shows device text with hidden characters escaped', () => {
		const s = summarize('tap', { warnings: ['a‮b\nc'] });
		expect(s.lines).toEqual(['Warning: a\\u{202e}b\\u{a}c']);
		expect(inert('plain <b>text</b>')).toBe('plain <b>text</b>');
	});
});

describe('the app logs model', () => {
	// Decision (step 6): device.logs for the applicationId, with no `since`, so the daemon's
	// default (the last launch) applies; and the typed id wins over the install panel's.
	it('reads the install panel\'s applicationId, or the typed one', async () => {
		const t = await setup();
		await t.logModel.load();
		expect(t.calls.calls.at(-1)?.params).toMatchObject({ device: 'emulator-5554', applicationId: 'com.x.app' });
		expect(t.calls.calls.at(-1)?.params).not.toHaveProperty('since');
		t.logModel.setApplicationId('com.y');
		await t.logModel.load();
		expect(t.calls.calls.at(-1)?.params).toMatchObject({ applicationId: 'com.y' });
		t.logModel.setApplicationId('');
		expect(t.logModel.view.applicationId).toBe('com.x.app');
	});

	// Decision: the crash summary is shown as text: exception, message, causes, frames.
	it('shows the crash summary and the lines as text', async () => {
		const t = await setup();
		t.calls.on('device.logs', () =>
			logs({
				crashes: [{ pid: 7, time: 't2', exception: 'java.lang.IllegalStateException', message: 'golden app crash', causes: ['java.lang.Foo: bar'], frames: ['com.x.A.b(A.kt:1)'] }],
				lines: [{ time: 't2', pid: 7, tid: 7, priority: 'fatal', tag: 'AndroidRuntime', message: '<script>x</script>', crash: true }],
			}),
		);
		await t.logModel.load();
		expect(t.logModel.view.crashes[0]?.lines).toEqual([
			'Crash of pid 7 at t2',
			'java.lang.IllegalStateException: golden app crash',
			'Caused by: java.lang.Foo: bar',
			'  at com.x.A.b(A.kt:1)',
		]);
		expect(t.logModel.view.lines).toEqual(['t2 7/7 F AndroidRuntime: <script>x</script> [crash]']);
		expect(crashView({ pid: 1, time: 't', exception: 'e', message: 'm' }).lines).toHaveLength(2);
		expect(logLine({ time: 't', pid: 1, tid: 2, priority: 'warn', tag: 'a‮', message: 'm' })).toBe('t 1/2 W a\\u{202e}: m');
	});

	// Decision (plan §Changes): a launch, install, tap or typed text by any client re-reads
	// shown logs; nothing shown, nothing read; a logs read is not its own trigger.
	it('re-reads shown logs on the triggers only', async () => {
		const t = await setup();
		await done(t, 'device.launch');
		expect(t.calls.names()).toEqual([]);
		await t.logModel.load();
		t.calls.calls.length = 0;
		for (const c of ['device.launch', 'device.install', 'device.tapElement', 'device.inputText']) {
			await done(t, c);
			expect(t.calls.names().filter((n) => n === 'device.logs'), c).toHaveLength(1);
			t.calls.calls.length = 0;
		}
		for (const c of ['device.logs', 'device.watchLogs', 'device.list']) await done(t, c);
		expect(t.calls.names().filter((n) => n === 'device.logs')).toEqual([]);
	});

	// Decision (ADR-0076, closing R2): the same filter as the tree's: another device's
	// operation re-reads nothing, this device's or an untargeted one does.
	it('re-reads shown logs only for an operation on its own device or on none', async () => {
		const t = await setup();
		await t.logModel.load();
		t.calls.calls.length = 0;
		await done(t, 'device.launch', undefined, { device: 'emulator-5556' });
		await done(t, 'device.tapElement', undefined, { device: 'emulator-5556' });
		expect(t.calls.names()).toEqual([]);
		await done(t, 'device.launch', undefined, { device: 'emulator-5554' });
		expect(t.calls.names().filter((n) => n === 'device.logs')).toHaveLength(1);
		t.calls.calls.length = 0;
		await done(t, 'device.launch');
		expect(t.calls.names().filter((n) => n === 'device.logs')).toHaveLength(1);
	});

	// Decision: logs belong to the device they were read from.
	it('forgets the logs when another device is selected', async () => {
		const t = await setup();
		await t.logModel.load();
		t.sel.select('emulator-5556');
		expect(t.logModel.view.lines).toEqual([]);
		expect(t.logModel.view.loaded).toBe(false);
	});

	// Decision: without an applicationId nothing is read.
	it('reads nothing without an applicationId', async () => {
		const t = await setup();
		t.app.view = { applicationId: '' };
		await t.logModel.load();
		expect(t.calls.names()).toEqual([]);
	});
});
