import { render } from 'svelte/server';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Params, Result } from '../api/capabilities.gen';
import { CapabilityError, type CallOptions } from '../api/client';
import { settle } from '../build/testing';
import { FetchModel, type FetchActivity, type FetchClient } from './fetch';
import GitToolbar from './GitToolbar.svelte';
import HistoryPanel from './HistoryPanel.svelte';
import { chain, refsOf, setup } from './historytesting';
import type { GitWatch } from './watch';

// Fetch (plan m2n P2): the model, the toolbar's and the Remotes section's controls.

interface Call {
	name: string;
	params: unknown;
	workspace: string | undefined;
}

/** A client whose git.fetch stays open until the test ends it. */
function fake() {
	const calls: Call[] = [];
	let fetchOpts: CallOptions | undefined;
	let finish: { resolve: (r: Result<'git.fetch'>) => void; reject: (e: unknown) => void } | null = null;
	const client: FetchClient = {
		call: ((name: string, params: unknown, opts?: CallOptions) => {
			calls.push({ name, params, workspace: opts?.workspace });
			if (name === 'git.fetch') {
				fetchOpts = opts;
				return new Promise((resolve, reject) => (finish = { resolve: resolve as never, reject }));
			}
			return Promise.resolve({});
		}) as FetchClient['call'],
	};
	return {
		calls,
		client,
		names: () => calls.map((c) => c.name),
		started: (id: string) => fetchOpts?.onOperation?.(id),
		ok: (remotes = ['origin']) => finish?.resolve({ remotes } as Result<'git.fetch'>),
		fail: (e: unknown) => finish?.reject(e),
	};
}

function activity() {
	const states = new Map<string, string>();
	const listeners = new Set<() => void>();
	const a: FetchActivity = {
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

const refused = () =>
	new CapabilityError(
		{ code: 'WORKSPACE_UNTRUSTED', message: 'the project root "/p" is not trusted', nextStep: "ask the user to run `droidyard trust '/p'`" },
		false,
	);

describe('FetchModel', () => {
	// Decision (ADR-0071 §1): Fetch is one git.fetch call: none named means every remote, a name means that one; the workspace is the project's root. Success reloads the refs once and leaves nothing running.
	it('calls git.fetch once, for all or for one remote, and reloads the refs on success', async () => {
		const f = fake();
		let reloads = 0;
		const m = new FetchModel(f.client, { root: ' /p ' }, () => reloads++);
		const run = m.run();
		expect(f.calls).toEqual([{ name: 'git.fetch', params: {}, workspace: '/p' }]);
		expect(m.view).toMatchObject({ state: 'running', busy: true, remote: null });
		f.ok();
		await run;
		expect(m.view).toMatchObject({ state: 'idle', busy: false, failure: null });
		expect(reloads).toBe(1);

		const one = m.run('origin');
		expect(f.calls.at(-1)).toEqual({ name: 'git.fetch', params: { remote: 'origin' }, workspace: '/p' });
		expect(m.view.remote).toBe('origin');
		f.ok();
		await one;
		expect(reloads).toBe(2);
	});

	// Decision (ADR-0071 §4): one write at a time; a second click while one is in flight makes no call.
	it('ignores a fetch asked for while one is in flight', async () => {
		const f = fake();
		const m = new FetchModel(f.client, { root: '/p' }, () => {});
		const first = m.run();
		void m.run('origin');
		void m.run();
		expect(f.names()).toEqual(['git.fetch']);
		f.ok();
		await first;
	});

	// Decision: the fetch is shown queued when the daemon holds it behind another write on the repository, running otherwise, following the activity feed; the feed is listened to only while it matters.
	it('is queued or running as the activity feed says, and stops listening at the end', async () => {
		const f = fake();
		const act = activity();
		const m = new FetchModel(f.client, { root: '/p' }, () => {}, act.a);
		const run = m.run();
		expect(act.listeners()).toBe(1);
		f.started('op-1');
		expect(m.view.state).toBe('running');
		act.set('op-1', 'queued');
		expect(m.view).toMatchObject({ state: 'queued', busy: true });
		act.set('op-1', 'running');
		expect(m.view.state).toBe('running');
		f.ok();
		await run;
		expect(act.listeners()).toBe(0);
	});

	// Decision: Cancel is operation.cancel on the operation this fetch started, never another; a cancelled fetch is not a failure and reloads nothing.
	it('cancels its own operation and shows no failure', async () => {
		const f = fake();
		let reloads = 0;
		const m = new FetchModel(f.client, { root: '/p' }, () => reloads++);
		const run = m.run();
		f.started('op-7');
		m.cancel();
		await settle();
		expect(f.calls.filter((c) => c.name === 'operation.cancel')).toEqual([{ name: 'operation.cancel', params: { operationId: 'op-7' }, workspace: undefined }]);
		expect(m.view.cancelling).toBe(true);
		m.cancel();
		await settle();
		expect(f.names().filter((n) => n === 'operation.cancel')).toHaveLength(1);
		f.fail(new CapabilityError({ code: 'OPERATION_CANCELLED', message: 'the operation was cancelled' }, false));
		await run;
		expect(m.view).toMatchObject({ state: 'idle', failure: null, cancelling: false });
		expect(reloads).toBe(0);
	});

	// Decision: a cancel pressed before the operation's id is known is sent when it arrives.
	it('sends a cancel asked for before the operation id is known when it arrives', async () => {
		const f = fake();
		const m = new FetchModel(f.client, { root: '/p' }, () => {});
		const run = m.run();
		m.cancel();
		await settle();
		expect(f.names()).toEqual(['git.fetch']);
		f.started('op-late');
		await settle();
		expect(f.calls.at(-1)).toMatchObject({ name: 'operation.cancel', params: { operationId: 'op-late' } });
		f.fail(new CapabilityError({ code: 'OPERATION_CANCELLED', message: 'cancelled' }, false));
		await run;
	});

	// Decision (ADR-0071 §2): an untrusted root is a refusal the user reads, with its next step; the button is not disabled by it (another click calls again), and the refs are not reloaded. Dismiss forgets it.
	it('keeps a refusal with its message and next step, and lets the next click call again', async () => {
		const f = fake();
		let reloads = 0;
		const m = new FetchModel(f.client, { root: '/p' }, () => reloads++);
		const run = m.run();
		f.fail(refused());
		await run;
		expect(m.view.state).toBe('failed');
		expect(m.view.busy).toBe(false);
		expect(m.view.failure).toEqual({ code: 'WORKSPACE_UNTRUSTED', message: 'the project root "/p" is not trusted', nextStep: "ask the user to run `droidyard trust '/p'`" });
		expect(reloads).toBe(0);
		const again = m.run('origin');
		expect(f.names()).toEqual(['git.fetch', 'git.fetch']);
		expect(m.view.failure).toBeNull();
		f.ok();
		await again;
		const bad = m.run();
		f.fail(refused());
		await bad;
		m.dismiss();
		expect(m.view).toMatchObject({ state: 'idle', failure: null });
	});

	// Decision (ADR-0071 §5): git's own words, the cause, are kept for the failure to show.
	it('keeps the cause of a failed fetch', async () => {
		const f = fake();
		const m = new FetchModel(f.client, { root: '/p' }, () => {});
		const run = m.run();
		f.fail(new CapabilityError({ code: 'GIT_FAILED', message: 'git failed', cause: 'fatal: could not read Username', nextStep: 'run git fetch once in a terminal' }, false));
		await run;
		expect(m.view.failure).toEqual({ code: 'GIT_FAILED', message: 'git failed', cause: 'fatal: could not read Username', nextStep: 'run git fetch once in a terminal' });
	});
});

describe('the Fetch controls', () => {
	afterEach(() => vi.unstubAllGlobals());
	const tag = (body: string, attr: string) => new RegExp(`<button[^>]*${attr}[^>]*>`).exec(body)?.[0] ?? '';
	const idle = { state: 'idle', remote: null, busy: false, failure: null, cancelling: false } as const;
	const toolbar = (fetchView: Parameters<typeof GitToolbar>[1]['fetchView']) =>
		render(GitToolbar, { props: { head: refsOf(['a']).head, onnavigate: () => {}, fetchView } }).body;

	// Decision (owner's item 10, ADR-0071 §2): the toolbar's Fetch is enabled (also where the root is untrusted: the refusal says why), carries its state for the level-4 check and has no Cancel while idle.
	it('renders the toolbar Fetch enabled, with its state and no Cancel while idle', () => {
		const body = toolbar(idle);
		expect(tag(body, 'data-toolbar="fetch"')).not.toMatch(/\sdisabled/);
		expect(tag(body, 'data-toolbar="fetch"')).toContain('data-op-state="idle"');
		expect(body).not.toContain('data-op-cancel');
		expect(tag(body, 'data-toolbar="pull"')).toMatch(/\sdisabled/);
	});

	// Decision: while a fetch is in flight the Fetch is disabled and says Queued or Fetching, and a Cancel appears.
	it('renders a fetch in flight as disabled, queued or running, with Cancel', () => {
		const running = toolbar({ ...idle, state: 'running', busy: true });
		expect(tag(running, 'data-toolbar="fetch"')).toMatch(/\sdisabled/);
		expect(tag(running, 'data-toolbar="fetch"')).toContain('data-op-state="running"');
		expect(running).toContain('Fetching…');
		expect(running).toContain('data-op-cancel');
		const queued = toolbar({ ...idle, state: 'queued', busy: true });
		expect(tag(queued, 'data-toolbar="fetch"')).toContain('data-op-state="queued"');
		expect(queued).toContain('Queued…');
	});

	/** A model driven into a state: `running` and `queued` stay in flight, `failed` has ended. */
	async function fetcherIn(state: 'idle' | 'running' | 'queued' | 'failed', remote?: string) {
		const f = fake();
		const act = activity();
		const m = new FetchModel(f.client, { root: '/p' }, () => {}, act.a);
		if (state === 'idle') return m;
		const run = m.run(remote);
		if (state === 'queued') {
			f.started('op-q');
			act.set('op-q', 'queued');
		}
		if (state === 'failed') {
			f.fail(new CapabilityError({ code: 'GIT_FAILED', message: 'git failed', cause: 'fatal: <img src=x onerror=1>', nextStep: 'run `git fetch` once in a terminal' }, false));
			await run;
		}
		return m;
	}

	async function panel(fetcher: FetchModel) {
		vi.stubGlobal('window', {});
		const t = setup(() => chain(3), 10);
		await settle();
		t.state.refs = refsOf(['c0'], {
			remotes: [
				{ remote: 'origin', name: 'main', oid: 'c0' },
				{ remote: 'team/up', name: 'dev', oid: 'c1' },
			],
		});
		t.store.refreshRefs();
		await settle();
		return render(HistoryPanel, { props: { store: t.store, watch: t.watch as unknown as GitWatch, workspaceId: 'ws1', fetcher } }).body;
	}

	// Decision (plan P2): each remote of the Remotes section has a Fetch of its own, named by the remote, and the toolbar's Fetch reaches every remote.
	it('renders a Fetch on each remote of the Remotes section', async () => {
		const body = await panel(await fetcherIn('idle'));
		expect([...body.matchAll(/data-fetch-remote="([^"]+)"/g)].map((m) => m[1])).toEqual(['origin', 'team/up']);
		expect(tag(body, 'data-fetch-remote="origin"')).not.toMatch(/\sdisabled/);
		expect(tag(body, 'data-fetch-remote="origin"')).toContain('data-op-state="idle"');
		expect(tag(body, 'data-toolbar="fetch"')).not.toMatch(/\sdisabled/);
		expect(body).not.toContain('data-op-cancel');
	});

	// Decision: while a fetch of one remote runs, every Fetch is disabled and only that remote's shows its state and a Cancel beside it; the toolbar's Fetch shows the state too.
	it('shows the running remote and disables every Fetch', async () => {
		const body = await panel(await fetcherIn('running', 'origin'));
		expect(tag(body, 'data-fetch-remote="origin"')).toMatch(/\sdisabled/);
		expect(tag(body, 'data-fetch-remote="origin"')).toContain('data-op-state="running"');
		expect(tag(body, 'data-fetch-remote="team/up"')).toMatch(/\sdisabled/);
		expect(tag(body, 'data-fetch-remote="team/up"')).toContain('data-op-state="idle"');
		expect(tag(body, 'data-toolbar="fetch"')).toMatch(/\sdisabled/);
		expect([...body.matchAll(/data-op-cancel/g)].length).toBeGreaterThanOrEqual(2);
	});

	// Decision: a fetch the daemon holds behind another write is drawn queued.
	it('draws a queued fetch as queued', async () => {
		const body = await panel(await fetcherIn('queued'));
		expect(tag(body, 'data-toolbar="fetch"')).toContain('data-op-state="queued"');
		expect(body).toContain('Queued…');
	});

	// Decision (ADR-0071 §2, §5): the failure is shown as text, git's cause in a block of its own and the next step, never as markup.
	it('draws a failure as inert text with its cause and next step', async () => {
		const body = await panel(await fetcherIn('failed'));
		expect(body).toContain('data-op-error');
		expect(body).toContain('GIT_FAILED: git failed');
		expect(body).toContain('run `git fetch` once in a terminal');
		expect(body).not.toContain('<img src=x');
		expect(body).toContain('&lt;img src=x');
		expect(tag(body, 'data-toolbar="fetch"')).not.toMatch(/\sdisabled/);
	});
});

// The seam of a typed call, kept honest: git.fetch's parameter is `remote` and its result `remotes`.
const _params: Params<'git.fetch'> = { remote: 'origin' };
const _result: Result<'git.fetch'> = { remotes: ['origin'] };
void _params;
void _result;
