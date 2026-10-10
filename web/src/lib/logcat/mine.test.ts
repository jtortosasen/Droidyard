import { describe, expect, it } from 'vitest';
import { MineSet } from './mine';
import { BuildModel, type BuildClient } from '../build/build';
import { activityWith, FakeCalls, hubStarted, settle, stateEvent } from '../build/testing';

class FakeWs {
	id: string | null = 'w1';
	root = '/p';
	trusted = true;
	subscribe() {
		return () => {};
	}
}

async function setup(lastResult: () => unknown) {
	const act = await activityWith([]);
	const { hub } = await hubStarted();
	const calls = new FakeCalls();
	calls.on('project.modules', () => ({ modules: [{ path: ':app', name: 'app', dir: 'app' }] }));
	calls.on('project.variants', () => ({ modules: [{ module: ':app', variants: ['debug'], source: 'probe' }] }));
	calls.on('build.lastResult', lastResult);
	calls.on('build.errors', (p) => ({ buildId: (p as { buildId: string }).buildId, outcome: 'succeeded', errors: [] }));
	const build = new BuildModel(calls as unknown as BuildClient, act.store, hub, new FakeWs());
	const installs = new Set<(id: string) => void>();
	const install = {
		subscribeInstalled(fn: (id: string) => void) {
			installs.add(fn);
			return () => installs.delete(fn);
		},
	};
	await settle();
	const mine = new MineSet(build, install);
	return { ...act, calls, build, mine, installed: (id: string) => installs.forEach((f) => f(id)) };
}

const result = (buildId: string, ...ids: string[]) => ({
	buildId,
	request: { tasks: [':app:assembleDebug'], module: ':app', variant: 'debug' },
	outcome: 'succeeded',
	durationMs: 1,
	outputs: ids.map((applicationId) => ({ module: ':app', variant: 'debug', applicationId, apk: 'a.apk' })),
});

describe('MineSet (m2e D39)', () => {
	// Decision (D39): the last result read at load counts, a new build adds its ids, an
	// install adds its id; nothing else does (not the project file's id, not another
	// workspace's build, whose end the build model does not read).
	it('holds the last result, then a new build and an install', async () => {
		let current = result('b1', 'com.one');
		const t = await setup(() => current);
		expect([...t.mine.ids]).toEqual(['com.one']);
		let heard = 0;
		t.mine.subscribe(() => heard++);
		current = result('b2', 'com.two');
		stateEvent(t.watch, 'op-b', { state: 'completed' });
		await settle();
		await settle();
		expect([...t.mine.ids].sort()).toEqual(['com.one', 'com.two']);
		t.installed('com.three');
		expect([...t.mine.ids].sort()).toEqual(['com.one', 'com.three', 'com.two']);
		expect(heard).toBe(2);
		t.installed('com.three'); // already known: no notification
		expect(heard).toBe(2);
	});

	it('is empty before any build, and does not count the build of another workspace', async () => {
		let current: unknown = null;
		const t = await setup(() => {
			if (current === null) throw Object.assign(new Error('none'), {});
			return current;
		});
		expect(t.mine.ids.size).toBe(0);
		current = result('b9', 'com.other');
		const reads = t.calls.names().filter((n) => n === 'build.lastResult').length;
		stateEvent(t.watch, 'op-o', { state: 'completed' }, 'w2');
		await settle();
		expect(t.calls.names().filter((n) => n === 'build.lastResult').length).toBe(reads);
		expect(t.mine.ids.size).toBe(0);
	});

	// Decision (D39): the project file's id is not in the set: the set takes ids from the
	// two sources above and the constructor takes nothing else.
	it('takes no project file id: only the build and install sources exist', async () => {
		const t = await setup(() => result('b1'));
		expect(t.mine.ids.size).toBe(0);
		expect(MineSet.length).toBe(2);
	});
});
