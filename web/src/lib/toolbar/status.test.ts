import { describe, expect, it } from 'vitest';
import { BuildMirror } from '../build/mirror';
import type { TasksView } from '../build/tasks';
import { activityWith, liveOp } from '../build/testing';
import { buildStatus } from './status';
import { selectionLabel, selectorState } from './selector';

const START = Date.parse('2026-10-10T10:00:00Z');
const noProgress = { progress: () => undefined, subscribe: () => () => {} };

async function building(over: Record<string, unknown> = {}, state: 'queued' | 'running' = 'running') {
	const { store } = await activityWith([liveOp('op1', state, over)]);
	const mirror = new BuildMirror(store, noProgress, { call: () => Promise.reject(new Error('unused')) }, () => 'w1');
	return mirror.view.building;
}

describe('the build status', () => {
	// Decision: any client's build shows, with the agent's name, and the clock is an argument.
	// The module is unknown for another client's build (ADR-0043 §2), so none is named.
	it("shows an agent's build as 'Building · 12 s' with the agent's name", async () => {
		const b = await building({ session: { id: 's1', name: 'claude' } });
		const s = buildStatus(b, { module: ':app', now: START + 12_000, labels: new Map() });
		expect(s?.text).toBe('Building · 12 s');
		expect(s?.actor).toBe('claude');
	});

	// Decision (D30): the session's label from the projects panel wins, so two agents of the same name stay told apart.
	it('uses the projects panel label for the agent', async () => {
		const b = await building({ session: { id: 's2', name: 'claude' } });
		const s = buildStatus(b, { module: ':app', now: START, labels: new Map([['s2', 'claude 2']]) });
		expect(s?.actor).toBe('claude 2');
	});

	// Decision: the user's own build names no actor; a queued one says queued; tasks name no module.
	it('names no actor for the tab own build, says queued, and names no module for build.run', async () => {
		const own = buildStatus(await building({ client: 'ui' }), { module: ':app', now: START + 3000, labels: new Map() });
		expect(own?.actor).toBeNull();
		expect(own?.text).toBe('Building :app · 3 s');
		const q = buildStatus(await building({ client: 'ui' }, 'queued'), { module: ':app', now: START + 3000, labels: new Map() });
		expect(q?.text).toBe('Queued :app · 3 s');
		expect(q?.queued).toBe(true);
		const run = buildStatus(await building({ capability: 'build.run' }), { module: ':app', now: START + 5000, labels: new Map() });
		expect(run?.text).toBe('Running tasks · 5 s');
	});

	it('is null when nothing builds', () => {
		expect(buildStatus(null, { module: ':app', now: 0, labels: new Map() })).toBeNull();
	});
});

const view = (over: Partial<TasksView> = {}): TasksView =>
	({ open: true, trusted: true, mirror: { building: null, cancelError: null }, tasks: [], filter: '', filtered: [], selected: [':app:assemble'], limitReached: false, loading: false, stale: false, loadError: null, runError: null, starting: false, ...over }) as TasksView;

describe('the task selector', () => {
	// Decision (ADR-0015 §5): untrusted, the selector is disabled and says why.
	it('is disabled with the reason when the project is untrusted', () => {
		const s = selectorState(view({ trusted: false }));
		expect(s.disabled).toBe(true);
		expect(s.reason).toMatch(/Trust this project/);
		expect(s.runDisabled).toBe(true);
	});

	it('is disabled with a reason when no project is open', () => {
		expect(selectorState(view({ open: false })).reason).toMatch(/Open a project/);
		expect(selectorState(null).disabled).toBe(true);
	});

	// Decision: Run waits while a build runs (any client's), and for a choice.
	it('disables Run while a build runs or nothing is chosen, not the list', () => {
		const busy = selectorState(view({ mirror: { building: {} as never, cancelError: null } }));
		expect(busy.disabled).toBe(false);
		expect(busy.runReason).toBe('A build is running');
		expect(selectorState(view({ selected: [] })).runReason).toBe('Choose a task first');
		const ok = selectorState(view());
		expect(ok.runDisabled).toBe(false);
		expect(ok.reason).toBeNull();
	});

	it('labels the selection', () => {
		expect(selectionLabel([])).toBe('Tasks');
		expect(selectionLabel([':app:assembleDebug'])).toBe('assembleDebug');
		expect(selectionLabel(['a', 'b'])).toBe('2 tasks');
	});
});
