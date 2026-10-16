import { describe, expect, it } from 'vitest';
import { BuildMirror } from './mirror';
import { MAX_TASKS, TasksModel, type TasksClient } from './tasks';
import { activityWith, capError, FakeCalls, liveOp, settle, stateEvent } from './testing';
import { hubStarted } from './testing';

class FakeWs {
	id: string | null = 'w1';
	root = '/p';
	trusted = true;
	gradleRoot: string | undefined = '.';
	#l = new Set<() => void>();
	subscribe(l: () => void) {
		this.#l.add(l);
		return () => this.#l.delete(l);
	}
	set(id: string | null) {
		this.id = id;
		for (const l of this.#l) l();
	}
	setGradleRoot(root: string | undefined) {
		this.gradleRoot = root;
		for (const l of this.#l) l();
	}
	trust() {
		this.trusted = true;
		for (const l of this.#l) l();
	}
}

const task = (n: number) => ({ module: ':app', path: `:app:t${n}`, name: `t${n}`, group: 'g', description: n === 3 ? 'Special one' : undefined });

async function tasksSetup(ops: ReturnType<typeof liveOp>[] = []) {
	const act = await activityWith(ops);
	const { hub } = await hubStarted();
	const calls = new FakeCalls();
	const all = Array.from({ length: 25 }, (_, i) => task(i + 1));
	calls.on('build.tasks', () => ({ tasks: all }));
	calls.on('build.run', () => ({ buildId: 'op-r' }));
	calls.on('operation.cancel', () => ({}));
	const ws = new FakeWs();
	const mirror = new BuildMirror(act.store, hub, calls as never, () => ws.id);
	const model = new TasksModel(calls as unknown as TasksClient, mirror, ws);
	await settle();
	calls.calls.length = 0;
	return { ...act, calls, ws, mirror, model };
}

describe('the tasks panel', () => {
	// Decision (ADR-0075 §7): the tasks are those of the Gradle root. A new root is a new
	// list, read once, with the old selection dropped; a describe that left the root as it
	// was reads nothing.
	it('reads the tasks again once when the Gradle root changes, and not on an unrelated describe', async () => {
		const t = await tasksSetup();
		t.model.toggle(':app:t1');
		t.ws.set('w1'); // a describe that changed nothing
		await settle();
		expect(t.calls.names()).not.toContain('build.tasks');
		t.ws.setGradleRoot('dev');
		await settle();
		expect(t.calls.names().filter((n) => n === 'build.tasks')).toHaveLength(1);
		expect(t.model.view.selected).toEqual([]);
		t.ws.set('w1');
		await settle();
		expect(t.calls.names().filter((n) => n === 'build.tasks')).toHaveLength(1);
	});

	// Decision (ADR-0075 §3): with no Gradle root the daemon answers PROJECT_NO_GRADLE_ROOT;
	// the panel shows that code and message, not a generic failure.
	it('shows PROJECT_NO_GRADLE_ROOT with its message', async () => {
		const t = await tasksSetup();
		t.calls.on('build.tasks', () => {
			throw capError('PROJECT_NO_GRADLE_ROOT', 'No settings.gradle(.kts) found');
		});
		t.ws.setGradleRoot(undefined);
		await settle();
		expect(t.model.view.loadError).toMatchObject({ code: 'PROJECT_NO_GRADLE_ROOT', message: 'No settings.gradle(.kts) found' });
	});

	// Decision (ADR-0045 §5): a grant publishes no event; the list the untrusted workspace
	// refused is read again when the workspace says it is trusted (level 4 kept the error).
	it('reads the tasks again when the workspace becomes trusted', async () => {
		const t = await tasksSetup();
		t.ws.trusted = false;
		t.ws.set('w1');
		await settle();
		t.calls.calls.length = 0;
		t.ws.trust();
		await settle();
		expect(t.calls.calls.map((c) => c.name)).toContain('build.tasks');
	});


	// Decision (D16): at most 20 tasks per build.run; the 21st selection is refused, and
	// unselecting makes room again.
	it('refuses the 21st selected task', async () => {
		const t = await tasksSetup();
		for (let i = 1; i <= MAX_TASKS; i++) t.model.toggle(`:app:t${i}`);
		expect(t.model.view.selected).toHaveLength(20);
		t.model.toggle(':app:t21');
		expect(t.model.view.selected).toHaveLength(20);
		expect(t.model.view.selected).not.toContain(':app:t21');
		expect(t.model.view.limitReached).toBe(true);
		t.model.toggle(':app:t1');
		expect(t.model.view.limitReached).toBe(false);
		t.model.toggle(':app:t21');
		expect(t.model.view.selected).toContain(':app:t21');
	});

	// Decision: Run sends build.run {tasks} with the selection in the order chosen, in the
	// open workspace; with nothing selected it sends nothing.
	it('runs the selection as build.run {tasks}', async () => {
		const t = await tasksSetup();
		await t.model.run();
		expect(t.calls.calls).toEqual([]);
		t.model.toggle(':app:t5');
		t.model.toggle(':app:t2');
		await t.model.run();
		expect(t.calls.calls).toEqual([{ name: 'build.run', params: { tasks: [':app:t5', ':app:t2'] }, workspace: '/p' }]);
	});

	// Decision (D11): building is the mirror's, whoever started it: an mcp build disables
	// Run (no call) and Cancel is operation.cancel on that operation.
	it('shares the mirror: another client\'s build blocks Run and Cancel cancels it', async () => {
		const t = await tasksSetup([liveOp('op-mcp', 'running', { capability: 'build.run' })]);
		expect(t.model.view.mirror.building?.client).toBe('mcp');
		t.model.toggle(':app:t1');
		await t.model.run();
		expect(t.calls.names()).not.toContain('build.run');
		await t.model.mirror.cancel();
		expect(t.calls.calls).toEqual([{ name: 'operation.cancel', params: { operationId: 'op-mcp' }, workspace: undefined }]);
	});

	// Decision: a refusal of build.run is shown with its code.
	it('reports a refused run with its code', async () => {
		const t = await tasksSetup();
		t.calls.on('build.run', () => {
			throw capError('WORKSPACE_UNTRUSTED');
		});
		t.model.toggle(':app:t1');
		await t.model.run();
		expect(t.model.view.runError?.code).toBe('WORKSPACE_UNTRUSTED');
		expect(t.model.view.starting).toBe(false);
	});

	// Decision: the filter matches path and description, case-insensitively, and keeps the selection.
	it('filters by text without losing the selection', async () => {
		const t = await tasksSetup();
		t.model.toggle(':app:t1');
		t.model.setFilter('SPECIAL');
		expect(t.model.view.filtered.map((x) => x.path)).toEqual([':app:t3']);
		expect(t.model.view.selected).toEqual([':app:t1']);
	});
});
