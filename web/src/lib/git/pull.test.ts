import { render } from 'svelte/server';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Params, Result } from '../api/capabilities.gen';
import { CapabilityError, type CallOptions } from '../api/client';
import { settle } from '../build/testing';
import GitToolbar from './GitToolbar.svelte';
import HistoryPanel from './HistoryPanel.svelte';
import { chain, idleBranches, idleUpdater, refsOf, setup, submodulesOf } from './historytesting';
import { FetchModel } from './fetch';
import { PullModel, pullBlockText, summarise, summaryText, type PullActivity, type PullClient } from './pull';
import type { GitWatch } from './watch';

// Pull (plan m2n R3; ADR-0074 §1, §2): the model and the toolbar's controls.

interface Call {
	name: string;
	params: unknown;
	workspace: string | undefined;
}

const BEFORE = 'a1b2c3d4e5f60718293a4b5c6d7e8f9012345678';
const AFTER = 'e4f5a6b7c8d9e0f1a2b3c4d5e6f708192a3b4c5d';

/** A client whose git.pull stays open until the test ends it. */
function fake() {
	const calls: Call[] = [];
	let pullOpts: CallOptions | undefined;
	let finish: { resolve: (r: Result<'git.pull'>) => void; reject: (e: unknown) => void } | null = null;
	const client: PullClient = {
		call: ((name: string, params: unknown, opts?: CallOptions) => {
			calls.push({ name, params, workspace: opts?.workspace });
			if (name === 'git.pull') {
				pullOpts = opts;
				return new Promise((resolve, reject) => (finish = { resolve: resolve as never, reject }));
			}
			return Promise.resolve({});
		}) as PullClient['call'],
	};
	return {
		calls,
		client,
		names: () => calls.map((c) => c.name),
		started: (id: string) => pullOpts?.onOperation?.(id),
		ok: (r: Result<'git.pull'> = { branch: 'main', before: BEFORE, after: AFTER }) => finish?.resolve(r),
		fail: (e: unknown) => finish?.reject(e),
	};
}

function activity() {
	const states = new Map<string, string>();
	const listeners = new Set<() => void>();
	const a: PullActivity = {
		stateOf: (id) => states.get(id),
		subscribe: (l) => (listeners.add(l), () => listeners.delete(l)),
	};
	return {
		a,
		set(id: string, state: string) {
			states.set(id, state);
			for (const l of [...listeners]) l();
		},
		listeners: () => listeners.size,
	};
}

const clean = { dirtyPaths: () => [] as string[] };

const diverged = () =>
	new CapabilityError(
		{ code: 'GIT_FAILED', message: 'git failed', cause: 'fatal: Not possible to fast-forward, aborting.', nextStep: 'merge or rebase it in a terminal, then retry' },
		false,
	);

describe('PullModel', () => {
	// Decision (ADR-0074 §1): Pull is one git.pull call with no parameter; the workspace is the project's root. Success keeps what the daemon answered and reloads the refs once.
	it('calls git.pull once with no parameter, keeps the result and reloads the refs', async () => {
		const f = fake();
		let reloads = 0;
		const m = new PullModel(f.client, { root: ' /p ' }, clean, () => reloads++);
		const run = m.run();
		expect(f.calls).toEqual([{ name: 'git.pull', params: {}, workspace: '/p' }]);
		expect(m.view).toMatchObject({ state: 'running', busy: true });
		f.ok();
		await run;
		expect(m.view).toMatchObject({ state: 'idle', busy: false, failure: null, result: { branch: 'main', upToDate: false, from: 'a1b2c3d', to: 'e4f5a6b' } });
		expect(reloads).toBe(1);
	});

	// Decision (ADR-0074 §1): equal ids mean the branch was up to date, and the result says so; otherwise it shows the short ids the branch moved between.
	it('says up to date for equal ids and the short ids otherwise', () => {
		expect(summaryText(summarise({ branch: 'main', before: BEFORE, after: BEFORE }))).toBe('main is up to date');
		expect(summarise({ branch: 'main', before: BEFORE, after: BEFORE })).toMatchObject({ upToDate: true, from: '', to: '' });
		expect(summaryText(summarise({ branch: 'dev', before: BEFORE, after: AFTER }))).toBe('dev a1b2c3d..e4f5a6b');
	});

	// Decision (ADR-0071 §4): one write at a time; a second click while one is in flight makes no call.
	it('ignores a pull asked for while one is in flight', async () => {
		const f = fake();
		const m = new PullModel(f.client, { root: '/p' }, clean, () => {});
		const first = m.run();
		void m.run();
		expect(f.names()).toEqual(['git.pull']);
		f.ok();
		await first;
	});

	// Decision (ADR-0074 §2, as for a checkout, ADR-0071 §5): with an unsaved editor buffer the pull calls nothing and names the files; once they are saved it runs. Seen failing with the gate removed from run: git.pull was called with an unsaved file.
	it('calls nothing while a buffer is unsaved, and names the files', async () => {
		const f = fake();
		let dirty = ['app/A.kt', 'app/B.kt'];
		const m = new PullModel(f.client, { root: '/p' }, { dirtyPaths: () => dirty }, () => {});
		await m.run();
		expect(f.calls).toEqual([]);
		expect(m.view).toMatchObject({ state: 'idle', busy: false, blocked: ['app/A.kt', 'app/B.kt'], blockedText: pullBlockText(['app/A.kt', 'app/B.kt']) });
		expect(m.view.blockedText).toContain('2 files have unsaved changes');
		dirty = [];
		const run = m.run();
		expect(f.names()).toEqual(['git.pull']);
		expect(m.view.blocked).toEqual([]);
		f.ok();
		await run;
		dirty = ['x'];
		await m.run();
		m.dismiss();
		expect(m.view).toMatchObject({ blocked: [], blockedText: '' });
	});

	// Decision: the pull is shown queued when the daemon holds it behind another write on the repository, running otherwise, following the activity feed; the feed is listened to only while it matters.
	it('is queued or running as the activity feed says, and stops listening at the end', async () => {
		const f = fake();
		const act = activity();
		const m = new PullModel(f.client, { root: '/p' }, clean, () => {}, act.a);
		const run = m.run();
		expect(act.listeners()).toBe(1);
		f.started('op-1');
		act.set('op-1', 'queued');
		expect(m.view).toMatchObject({ state: 'queued', busy: true });
		act.set('op-1', 'running');
		expect(m.view.state).toBe('running');
		f.ok();
		await run;
		expect(act.listeners()).toBe(0);
	});

	// Decision: Cancel is operation.cancel on the operation this pull started; a cancelled pull is not a failure and shows no result; the refs are read again, since its fetch may have run; a cancel pressed before the id is known is sent when it arrives.
	it('cancels its own operation and shows neither failure nor result', async () => {
		const f = fake();
		let reloads = 0;
		const m = new PullModel(f.client, { root: '/p' }, clean, () => reloads++);
		const run = m.run();
		m.cancel();
		await settle();
		expect(f.names()).toEqual(['git.pull']);
		f.started('op-7');
		await settle();
		expect(f.calls.at(-1)).toEqual({ name: 'operation.cancel', params: { operationId: 'op-7' }, workspace: undefined });
		expect(m.view.cancelling).toBe(true);
		f.fail(new CapabilityError({ code: 'OPERATION_CANCELLED', message: 'the operation was cancelled' }, false));
		await run;
		expect(m.view).toMatchObject({ state: 'idle', failure: null, result: null, cancelling: false });
		expect(reloads).toBe(1);
	});

	// Decision (ADR-0074 §2, ADR-0071 §5): a refusal is kept as the daemon said it, git's words as the cause and the next step, until the next run or a dismiss; the refs are read again, since a refused pull may have fetched (a diverged branch's upstream moved).
	it('keeps a failure with its cause and next step, and the next click clears it', async () => {
		const f = fake();
		let reloads = 0;
		const m = new PullModel(f.client, { root: '/p' }, clean, () => reloads++);
		const run = m.run();
		f.fail(diverged());
		await run;
		expect(m.view.state).toBe('failed');
		expect(m.view.busy).toBe(false);
		expect(m.view.failure).toEqual({ code: 'GIT_FAILED', message: 'git failed', cause: 'fatal: Not possible to fast-forward, aborting.', nextStep: 'merge or rebase it in a terminal, then retry' });
		expect(reloads).toBe(1);
		const again = m.run();
		expect(f.names()).toEqual(['git.pull', 'git.pull']);
		expect(m.view.failure).toBeNull();
		f.ok();
		await again;
		expect(m.view.result).not.toBeNull();
		m.dismiss();
		expect(m.view).toMatchObject({ state: 'idle', failure: null, result: null });
	});
});

describe('the Pull controls', () => {
	afterEach(() => vi.unstubAllGlobals());
	const tag = (body: string, attr: string) => new RegExp(`<button[^>]*${attr}[^>]*>`).exec(body)?.[0] ?? '';
	const idle = { state: 'idle', busy: false, failure: null, cancelling: false, result: null, blocked: [], blockedText: '' } as const;
	const behind = { oid: 'a', branch: 'main', detached: false, upstream: 'origin/main', upstreamGone: false, ahead: 0, behind: 2 };
	const toolbar = (pullView: Parameters<typeof GitToolbar>[1]['pullView'], head = behind) =>
		render(GitToolbar, { props: { head, onnavigate: () => {}, pullView } }).body;

	// Decision (ADR-0074 §1): the toolbar's Pull is enabled, keeps its dot when the branch is behind, carries its state and has no Cancel or note while idle.
	it('renders the toolbar Pull enabled, with its dot and state, and no Cancel while idle', () => {
		const body = toolbar(idle);
		expect(tag(body, 'data-toolbar="pull"')).not.toMatch(/\sdisabled/);
		expect(tag(body, 'data-toolbar="pull"')).toContain('data-op-state="idle"');
		expect(tag(body, 'data-toolbar="pull"')).toContain('data-dot');
		expect(body).not.toContain('data-pull-cancel');
		expect(body).not.toContain('data-pull-result');
	});

	// Decision: while a pull is in flight the Pull is disabled and says Queued or Pulling, and a Cancel of its own appears (the Fetch's keeps its own marker).
	it('renders a pull in flight as disabled, queued or running, with Cancel', () => {
		const running = toolbar({ ...idle, state: 'running', busy: true });
		expect(tag(running, 'data-toolbar="pull"')).toMatch(/\sdisabled/);
		expect(tag(running, 'data-toolbar="pull"')).toContain('data-op-state="running"');
		expect(running).toContain('Pulling…');
		expect(running).toContain('data-pull-cancel');
		expect(running).not.toContain('data-op-cancel');
		const queued = toolbar({ ...idle, state: 'queued', busy: true });
		expect(tag(queued, 'data-toolbar="pull"')).toContain('data-op-state="queued"');
		expect(queued).toContain('Queued…');
		expect(tag(toolbar({ ...idle, state: 'running', busy: true, cancelling: true }), 'data-pull-cancel')).toMatch(/\sdisabled/);
	});

	// Decision (ADR-0074 §1): the result is shown: up to date, or the short ids the branch moved between.
	it('shows what the pull did', () => {
		const moved = toolbar({ ...idle, result: summarise({ branch: 'main', before: BEFORE, after: AFTER }) });
		expect(moved).toContain('data-pull-result');
		expect(moved).toContain('main a1b2c3d..e4f5a6b');
		const same = toolbar({ ...idle, result: summarise({ branch: 'main', before: BEFORE, after: BEFORE }) });
		expect(same).toContain('main is up to date');
		expect(same).toContain('data-up-to-date');
	});

	// Decision (ADR-0071 §5): the failure is inert text, git's cause in a block of its own and the next step; the blocked files are named.
	it('draws a failure and a block as text with their details', () => {
		const failure = { code: 'GIT_FAILED', message: 'git failed', cause: 'fatal: <img src=x onerror=1>', nextStep: 'merge or rebase it in a terminal' };
		const body = toolbar({ ...idle, state: 'failed', failure });
		expect(body).toContain('data-pull-error');
		expect(body).toContain('GIT_FAILED: git failed');
		expect(body).toContain('merge or rebase it in a terminal');
		expect(body).not.toContain('<img src=x');
		expect(body).toContain('&lt;img src=x');
		expect(tag(body, 'data-toolbar="pull"')).not.toMatch(/\sdisabled/);
		const blocked = toolbar({ ...idle, blocked: ['app/<b>.kt'], blockedText: pullBlockText(['app/<b>.kt']) });
		expect(blocked).toContain('data-pull-blocked');
		expect(blocked).toContain('app/&lt;b>.kt');
		expect(blocked).not.toContain('<b>.kt');
	});

	async function panel(puller: PullModel) {
		vi.stubGlobal('window', {});
		const t = setup(() => chain(3), 10);
		await settle();
		t.state.refs = refsOf(['c0']);
		t.store.refreshRefs();
		await settle();
		const submodules = await submodulesOf();
		const fetcher = new FetchModel({ call: () => Promise.resolve({}) } as never, { root: '/p' }, () => {});
		return render(HistoryPanel, {
			props: { store: t.store, watch: t.watch as unknown as GitWatch, workspaceId: 'ws1', fetcher, puller, branches: idleBranches(), submodules, updater: idleUpdater() },
		}).body;
	}

	// Decision: the History tab hands the toolbar its Pull model: a pull in flight and a failure both show in the tab.
	it('shows the model\'s state in the History tab', async () => {
		const f = fake();
		const m = new PullModel(f.client, { root: '/p' }, clean, () => {});
		const run = m.run();
		const inFlight = await panel(m);
		expect(tag(inFlight, 'data-toolbar="pull"')).toContain('data-op-state="running"');
		expect(inFlight).toContain('data-pull-cancel');
		f.fail(diverged());
		await run;
		const failed = await panel(m);
		expect(failed).toContain('data-pull-error');
		expect(failed).toContain('fatal: Not possible to fast-forward, aborting.');
	});
});

// The seam of a typed call, kept honest: git.pull takes no parameter and answers the branch and the two ids.
const _params: Params<'git.pull'> = {};
const _result: Result<'git.pull'> = { branch: 'main', before: BEFORE, after: AFTER };
void _params;
void _result;
