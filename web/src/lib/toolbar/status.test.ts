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
	it("shows an agent's build with the agent's name", async () => {
		const b = await building({ session: { id: 's1', name: 'claude' }, target: { module: ':app', variant: 'debug' } });
		const s = buildStatus(b, { now: START + 12_000, labels: new Map() });
		expect(s?.text).toBe('Building :app · 12 s');
		expect(s?.actor).toBe('claude');
	});

	// Decision (ADR-0076, closing R5): the module is the operation's own target, so an MCP
	// client's build.assemble names its module and this tab's selection is not consulted;
	// a build with no target names none rather than guess.
	it("names the module of any client's build.assemble from its target, never this tab's selection", async () => {
		const mcp = await building({ client: 'mcp', target: { module: ':feature', variant: 'release' } });
		expect(buildStatus(mcp, { now: START, labels: new Map() })?.text).toBe('Building :feature · 0 s');
		const cli = await building({ client: 'cli', target: { module: ':lib', variant: 'debug' } }, 'queued');
		expect(buildStatus(cli, { now: START, labels: new Map() })?.text).toBe('Queued :lib · 0 s');
		const untargeted = await building({ client: 'mcp' });
		expect(buildStatus(untargeted, { now: START, labels: new Map() })?.text).toBe('Building · 0 s');
	});

	// Decision (D30): the session's label from the projects panel wins, so two agents of the same name stay told apart.
	it('uses the projects panel label for the agent', async () => {
		const b = await building({ session: { id: 's2', name: 'claude' } });
		const s = buildStatus(b, { now: START, labels: new Map([['s2', 'claude 2']]) });
		expect(s?.actor).toBe('claude 2');
	});

	// Decision (ADR-0076): the module is the caller's string, so the toolbar shows it with
	// control and bidirectional characters escaped, as the devices mirror does.
	it('shows a module with a bidirectional override escaped', async () => {
		const b = await building({ target: { module: ':ap‮p', variant: 'debug' } });
		expect(buildStatus(b, { now: START, labels: new Map() })?.text).toBe('Building :ap\\u{202e}p · 0 s');
	});

	// Decision: the user's own build names no actor; a queued one says queued; tasks name no module.
	it('names no actor for the tab own build, says queued, and names no module for build.run', async () => {
		const own = buildStatus(await building({ client: 'ui', target: { module: ':app', variant: 'debug' } }), { now: START + 3000, labels: new Map() });
		expect(own?.actor).toBeNull();
		expect(own?.text).toBe('Building :app · 3 s');
		const q = buildStatus(await building({ client: 'ui', target: { module: ':app', variant: 'debug' } }, 'queued'), { now: START + 3000, labels: new Map() });
		expect(q?.text).toBe('Queued :app · 3 s');
		expect(q?.queued).toBe(true);
		const run = buildStatus(await building({ capability: 'build.run' }), { now: START + 5000, labels: new Map() });
		expect(run?.text).toBe('Running tasks · 5 s');
	});

	it('is null when nothing builds', () => {
		expect(buildStatus(null, { now: 0, labels: new Map() })).toBeNull();
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
