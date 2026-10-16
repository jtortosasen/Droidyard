import { describe, expect, it } from 'vitest';
import { BuildModel, type BuildClient } from './build';
import { activityWith, capError, FakeCalls, liveOp, settle, stateEvent, hubStarted } from './testing';

const outcome = (buildId: string, over: Record<string, unknown> = {}) => ({
	buildId,
	request: { tasks: [':app:assembleDebug'], module: ':app', variant: 'debug' },
	outcome: 'failed',
	durationMs: 10,
	...over,
});

class FakeWs {
	id: string | null;
	root = '/p';
	trusted = true;
	gradleRoot: string | undefined = '.';
	#l = new Set<() => void>();
	constructor(id: string | null) {
		this.id = id;
	}
	subscribe(l: () => void) {
		this.#l.add(l);
		return () => this.#l.delete(l);
	}
	set(id: string | null) {
		this.id = id;
		for (const l of this.#l) l();
	}
	trust() {
		this.trusted = true;
		for (const l of this.#l) l();
	}
	setGradleRoot(root: string | undefined) {
		this.gradleRoot = root;
		for (const l of this.#l) l();
	}
}

async function setup(ops: ReturnType<typeof liveOp>[] = [], lastResult?: () => unknown) {
	const act = await activityWith(ops);
	const { events, hub } = await hubStarted();
	const calls = new FakeCalls();
	calls.on('project.modules', () => ({ modules: [{ path: ':app', name: 'app', dir: 'app' }] }));
	calls.on('project.variants', () => ({ modules: [{ module: ':app', variants: ['debug', 'release'], source: 'probe' }] }));
	calls.on('build.lastResult', lastResult ?? (() => { throw capError('BUILD_NOT_FOUND'); }));
	calls.on('build.errors', (p) => ({ buildId: (p as { buildId: string }).buildId, outcome: 'failed', errors: [] }));
	calls.on('operation.cancel', () => ({}));
	calls.on('build.assemble', () => ({ buildId: 'op-x' }));
	const ws = new FakeWs('w1');
	const model = new BuildModel(calls as unknown as BuildClient, act.store, hub, ws);
	await settle();
	calls.calls.length = 0; // the load's own reads are not what these tests count
	return { ...act, events, hub, calls, ws, model };
}

describe('trust granted from the UI', () => {
	// Decision (ADR-0045 §5): a grant publishes no event, so the panel reads what the
	// untrusted workspace refused again when the workspace says it is trusted now
	// (level 4 showed WORKSPACE_UNTRUSTED kept after Trust).
	it('reads the modules and the last build again when the workspace becomes trusted', async () => {
		const t = await setup();
		t.ws.trusted = false;
		t.ws.set('w1'); // same id: nothing read, the untrusted mark recorded
		await settle();
		t.calls.calls.length = 0;
		t.ws.trust();
		await settle();
		const names = t.calls.calls.map((c) => c.name);
		expect(names).toContain('project.modules');
		expect(names).toContain('build.lastResult');
	});
});

describe('the Gradle root changes (ADR-0075 §7)', () => {
	// Decision: the modules and variants are those of the Gradle root; a new root is read
	// again, once, and an unrelated describe (same root) reads nothing.
	it('reads the modules again once when the root changes, and not on an unrelated describe', async () => {
		const t = await setup();
		t.ws.set('w1'); // a describe that changed nothing
		await settle();
		expect(t.calls.names()).not.toContain('project.modules');
		t.ws.setGradleRoot('dev');
		await settle();
		expect(t.calls.names().filter((n) => n === 'project.modules')).toHaveLength(1);
		t.ws.set('w1');
		await settle();
		expect(t.calls.names().filter((n) => n === 'project.modules')).toHaveLength(1);
	});
});

describe('the build mirror', () => {
	// Decision (D11): a build of another client in this workspace shows as building with
	// its client kind, and Cancel is operation.cancel on that operation's id.
	it('shows an mcp build of this workspace as building (mcp) and cancels that operation', async () => {
		const t = await setup([liveOp('op-mcp', 'running')]);
		const b = t.model.view.mirror.building;
		expect(b?.client).toBe('mcp');
		expect(b?.state).toBe('running');
		await t.model.mirror.cancel();
		expect(t.calls.calls).toEqual([{ name: 'operation.cancel', params: { operationId: 'op-mcp' }, workspace: undefined }]);
		expect(t.calls.names()).not.toContain('operation.wait');
	});

	// Decision (D11): the mirror is per workspace; a build elsewhere is not this panel's.
	it('is idle for a build of another workspace', async () => {
		const t = await setup([liveOp('op-other', 'running', { workspace: 'w2' })]);
		expect(t.model.view.mirror.building).toBeNull();
		await t.model.build();
		expect(t.calls.names()).toEqual(['build.assemble']);
	});

	// Decision (D11): a queued build shows queued and what it was behind when recorded;
	// the panel does not claim the holder is current.
	it('shows a queued build with the blockedBy it was recorded with', async () => {
		const blockedBy = { operationId: 'op-holder', capability: 'build.run' };
		const t = await setup([liveOp('op-q', 'queued', { blockedBy })]);
		const b = t.model.view.mirror.building;
		expect(b?.state).toBe('queued');
		expect(b?.blockedBy).toEqual(blockedBy);
	});

	// Decision (D12): progress is routed by operation id; another operation's is ignored.
	it('shows the progress of the build shown and ignores that of other operations', async () => {
		const t = await setup([liveOp('op-b', 'running')]);
		t.events.emit(0, { topic: 'operation.progress', type: 'progress', operation: 'op-other', payload: { done: 9, total: 9 } });
		expect(t.model.view.mirror.building?.progress).toBeUndefined();
		t.events.emit(0, { topic: 'operation.progress', type: 'progress', operation: 'op-b', payload: { done: 2, total: 5, message: ':app:compile' } });
		expect(t.model.view.mirror.building?.progress).toEqual({ done: 2, total: 5, message: ':app:compile' });
	});

	// Decision: building disappears when the operation ends, whoever ran it.
	it('goes idle when the build ends', async () => {
		const t = await setup([liveOp('op-b', 'running')]);
		stateEvent(t.watch, 'op-b', { state: 'completed' });
		expect(t.model.view.mirror.building).toBeNull();
	});

	// Decision (D11): Build is not offered a second time while one is on; the daemon would queue it.
	it('does not start a build while one is shown', async () => {
		const t = await setup([liveOp('op-b', 'running')]);
		await t.model.build();
		expect(t.calls.names()).toEqual([]);
	});
});

describe('the reads (D13)', () => {
	// Decision: the end of a build of this workspace by any client reads the last result
	// and then its problems, once each, for the buildId the result names.
	it('reads build.lastResult then build.errors once when a build.assemble of this workspace ends', async () => {
		const t = await setup([liveOp('op-b', 'running')], () => outcome('op-b'));
		stateEvent(t.watch, 'op-b', { state: 'completed' });
		await settle();
		expect(t.calls.calls.map((c) => [c.name, c.params])).toEqual([
			['build.lastResult', {}],
			['build.errors', { buildId: 'op-b' }],
		]);
		expect(t.model.view.last?.buildId).toBe('op-b');
	});

	it('also reads when a build.run ends', async () => {
		const t = await setup([], () => outcome('op-r'));
		stateEvent(t.watch, 'op-r', { capability: 'build.run', state: 'completed' });
		await settle();
		expect(t.calls.names()).toEqual(['build.lastResult', 'build.errors']);
	});

	// Decision: a read is never its own trigger; a terminal build.lastResult, build.errors
	// or build.tasks of the UI causes no read (a broad build.* trigger would loop).
	it('does not read when the UI\'s own build.lastResult, build.errors or build.tasks end', async () => {
		const t = await setup();
		for (const [i, capability] of ['build.lastResult', 'build.errors', 'build.tasks'].entries()) {
			stateEvent(t.watch, `op-r${i}`, { capability, state: 'completed', client: 'ui' });
		}
		await settle();
		expect(t.calls.names()).toEqual([]);
	});

	// Decision: only this workspace's builds are a trigger.
	it('does not read when a build of another workspace ends', async () => {
		const t = await setup();
		stateEvent(t.watch, 'op-o', { state: 'completed' }, 'w2');
		await settle();
		expect(t.calls.names()).toEqual([]);
	});

	// Decision: BUILD_NOT_FOUND at load means there is no build yet, not a failure.
	it('says there is no build yet on BUILD_NOT_FOUND at load', async () => {
		const act = await activityWith();
		const { hub } = await hubStarted();
		const calls = new FakeCalls();
		calls.on('project.modules', () => ({ modules: [] }));
		calls.on('build.lastResult', () => {
			throw capError('BUILD_NOT_FOUND');
		});
		const model = new BuildModel(calls as unknown as BuildClient, act.store, hub, new FakeWs('w1'));
		await settle();
		expect(model.view.noBuild).toBe(true);
		expect(model.view.loadError).toBeNull();
		expect(calls.names()).not.toContain('build.errors');
	});

	// Decision: another error at load is shown with its code, not hidden as "no build".
	it('shows a refused read with its code', async () => {
		const act = await activityWith();
		const { hub } = await hubStarted();
		const calls = new FakeCalls();
		calls.on('project.modules', () => ({ modules: [] }));
		calls.on('build.lastResult', () => {
			throw capError('WORKSPACE_UNTRUSTED');
		});
		const model = new BuildModel(calls as unknown as BuildClient, act.store, hub, new FakeWs('w1'));
		await settle();
		expect(model.view.noBuild).toBe(false);
		expect(model.view.loadError?.code).toBe('WORKSPACE_UNTRUSTED');
	});

	// Decision: the problems the daemon returns reach the panel unchanged (merged, tail).
	it('keeps the problems of the build, merged and unrecognised included', async () => {
		const t = await setup([liveOp('op-b', 'running')], () => outcome('op-b'));
		t.calls.on('build.errors', () => ({
			buildId: 'op-b',
			outcome: 'failed',
			errors: [{ file: 'A.kt', line: 3, column: 7, merged: true, message: 'm' }],
			unrecognised: true,
			tail: 'console tail',
		}));
		stateEvent(t.watch, 'op-b', { state: 'completed' });
		await settle();
		expect(t.model.view.problems?.errors[0]).toMatchObject({ file: 'A.kt', line: 3, column: 7, merged: true });
		expect(t.model.view.problems?.tail).toBe('console tail');
	});

	// Decision: the UI's own build is read back like any other client's: no read until it ends.
	it('starts a build with the chosen module and variant and reads only when it ends', async () => {
		const t = await setup([], () => outcome('op-x'));
		await t.model.build();
		expect(t.calls.calls.map((c) => [c.name, c.params, c.workspace])).toEqual([
			['build.assemble', { module: ':app', variant: 'debug' }, '/p'],
		]);
		stateEvent(t.watch, 'op-x', { state: 'completed', client: 'ui' });
		await settle();
		expect(t.calls.names()).toEqual(['build.assemble', 'build.lastResult', 'build.errors']);
	});

	// Decision: a refused Build shows the code and its next step.
	it('shows a refused build', async () => {
		const t = await setup();
		t.calls.on('build.assemble', () => {
			throw capError('WORKSPACE_UNTRUSTED');
		});
		await t.model.build();
		expect(t.model.view.startError?.code).toBe('WORKSPACE_UNTRUSTED');
	});
});

describe('the workspace changing', () => {
	// Decision: with no open workspace nothing is mirrored or read; reopening loads again.
	it('clears when the workspace closes and loads again when it opens', async () => {
		const t = await setup([liveOp('op-b', 'running')], () => outcome('op-b'));
		t.ws.set(null);
		expect(t.model.view.open).toBe(false);
		expect(t.model.view.mirror.building).toBeNull();
		t.ws.set('w1');
		await settle();
		expect(t.calls.names()).toContain('project.modules');
		expect(t.model.view.mirror.building?.operationId).toBe('op-b');
	});
});
