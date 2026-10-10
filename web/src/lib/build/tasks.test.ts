import { describe, expect, it } from 'vitest';
import { BuildMirror } from './mirror';
import { MAX_TASKS, TasksModel, type TasksClient } from './tasks';
import { ConsoleModel, PAGE, type ConsoleClient, type ConsoleSource } from './console';
import { activityWith, capError, FakeCalls, liveOp, settle, stateEvent } from './testing';
import { hubStarted } from './testing';

class FakeWs {
	id: string | null = 'w1';
	root = '/p';
	#l = new Set<() => void>();
	subscribe(l: () => void) {
		this.#l.add(l);
		return () => this.#l.delete(l);
	}
	set(id: string | null) {
		this.id = id;
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

class FakeConsoleSource implements ConsoleSource {
	view = { open: true, last: null as { buildId: string; consoleRef: string } | null, mirror: { building: null as unknown } };
	#l = new Set<() => void>();
	subscribe(l: () => void) {
		this.#l.add(l);
		return () => this.#l.delete(l);
	}
	set(over: Partial<FakeConsoleSource['view']>) {
		this.view = { ...this.view, ...over };
		for (const l of this.#l) l();
	}
}

function consoleSetup() {
	const calls = new FakeCalls();
	const src = new FakeConsoleSource();
	const ws = new FakeWs();
	return { calls, src, ws, mk: () => new ConsoleModel(calls as unknown as ConsoleClient, src, ws) };
}

describe('the console', () => {
	// Decision (D14): the tail first, as artifact.read {ref, offset: -262144, limit: 262144}.
	it('reads the tail of the last build\'s console first', async () => {
		const t = consoleSetup();
		t.calls.on('artifact.read', () => ({ ref: 'r', mediaType: 'text/plain', offset: 1000, size: 2000, text: 'tail\nBUILD SUCCESSFUL\n' }));
		t.src.view.last = { buildId: 'b1', consoleRef: 'ref-1' };
		const m = t.mk();
		await settle();
		expect(t.calls.calls).toEqual([{ name: 'artifact.read', params: { ref: 'ref-1', offset: -PAGE, limit: PAGE }, workspace: undefined }]);
		expect(m.view.text).toContain('BUILD SUCCESSFUL');
		expect(m.view.hasEarlier).toBe(true);
	});

	// Decision (D14): "Earlier" reads from the previous offset minus the limit, ends where
	// the shown text starts, prepends, and stops at offset 0.
	it('pages back with the previous offset minus the limit and stops at 0', async () => {
		const t = consoleSetup();
		const offsets = [PAGE + 100, 100, 0];
		let n = 0;
		t.calls.on('artifact.read', (p) => {
			const params = p as { offset: number };
			const off = n === 0 ? offsets[0]! : params.offset;
			return { ref: 'r', mediaType: 'text/plain', offset: off, size: PAGE + 600, text: `page${n++}\n` };
		});
		t.src.view.last = { buildId: 'b1', consoleRef: 'ref-1' };
		const m = t.mk();
		await settle();
		await m.earlier();
		expect(t.calls.calls[1]!.params).toEqual({ ref: 'ref-1', offset: 100, limit: PAGE });
		expect(m.view.text).toBe('page1\npage0\n');
		expect(m.view.hasEarlier).toBe(true);
		await m.earlier();
		expect(t.calls.calls[2]!.params).toEqual({ ref: 'ref-1', offset: 0, limit: 100 });
		expect(m.view.offset).toBe(0);
		expect(m.view.hasEarlier).toBe(false);
		await m.earlier();
		expect(t.calls.calls).toHaveLength(3);
	});

	// Decision (plan step 4): no live console; while a build runs nothing is read and the
	// console says "after the build", then the new build's console is read.
	it('reads nothing while a build runs and reads the new console after it', async () => {
		const t = consoleSetup();
		t.calls.on('artifact.read', (p) => ({ ref: 'r', mediaType: 'text/plain', offset: 0, size: 5, text: (p as { ref: string }).ref }));
		t.src.view.last = { buildId: 'b1', consoleRef: 'ref-1' };
		const m = t.mk();
		await settle();
		t.src.set({ mirror: { building: { id: 'x' } } });
		expect(m.view.building).toBe(true);
		t.src.set({ last: { buildId: 'b2', consoleRef: 'ref-2' } });
		await settle();
		expect(t.calls.calls).toHaveLength(1); // b2 is not read while building
		t.src.set({ mirror: { building: null } });
		await settle();
		expect(t.calls.calls).toHaveLength(2);
		expect(m.view.text).toBe('ref-2');
		expect(m.view.building).toBe(false);
	});

	// Decision: a failed read shows its code and does not invent text.
	it('shows a failed read as an error', async () => {
		const t = consoleSetup();
		t.calls.on('artifact.read', () => {
			throw capError('ARTIFACT_NOT_FOUND');
		});
		t.src.view.last = { buildId: 'b1', consoleRef: 'ref-1' };
		const m = t.mk();
		await settle();
		expect(m.view.error?.code).toBe('ARTIFACT_NOT_FOUND');
		expect(m.view.text).toBe('');
	});
});
