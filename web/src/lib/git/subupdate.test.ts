import { render } from 'svelte/server';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Result } from '../api/capabilities.gen';
import { CapabilityError, type CallOptions } from '../api/client';
import { settle } from '../build/testing';
import { FetchModel, type FetchClient } from './fetch';
import HistoryPanel from './HistoryPanel.svelte';
import { chain, idleBranches, refsOf, setup, submodulesOf } from './historytesting';
import type { Submodule } from './submodules';
import { SubmoduleUpdateModel, dirtyUnder, summaryText, updateBlockText, type UpdateActivity, type UpdateClient } from './subupdate';
import type { GitWatch } from './watch';

// The Submodules section's Update (plan m2n P5): the dirty-buffer gate, the model and the markup the
// level-4 checks meet.
afterEach(() => vi.unstubAllGlobals());

interface Call {
	name: string;
	params: unknown;
	workspace: string | undefined;
}

/** A client whose git.updateSubmodules stays open until the test ends it. */
function fake() {
	const calls: Call[] = [];
	let opts: CallOptions | undefined;
	let finish: { resolve: (r: Result<'git.updateSubmodules'>) => void; reject: (e: unknown) => void } | null = null;
	const client: UpdateClient = {
		call: ((name: string, params: unknown, o?: CallOptions) => {
			calls.push({ name, params, workspace: o?.workspace });
			if (name === 'git.updateSubmodules') {
				opts = o;
				return new Promise((resolve, reject) => (finish = { resolve: resolve as never, reject }));
			}
			return Promise.resolve({});
		}) as UpdateClient['call'],
	};
	return {
		calls,
		client,
		names: () => calls.map((c) => c.name),
		started: (id: string) => opts?.onOperation?.(id),
		ok: (paths: string[] = []) => finish?.resolve({ paths } as Result<'git.updateSubmodules'>),
		fail: (e: unknown) => finish?.reject(e),
	};
}

function activity() {
	const states = new Map<string, string>();
	const listeners = new Set<() => void>();
	const a: UpdateActivity = {
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
	new CapabilityError({ code: 'WORKSPACE_UNTRUSTED', message: 'the project root "/p" is not trusted', nextStep: "ask the user to run `droidyard trust '/p'`" }, false);

function model(f: ReturnType<typeof fake>, dirty: string[] = [], onDone: () => void = () => {}, act?: UpdateActivity) {
	return new SubmoduleUpdateModel(f.client, { root: ' /p ' }, { dirtyPaths: () => dirty }, onDone, act ?? null);
}

describe('the dirty-buffer gate', () => {
	// Decision (ADR-0071 §5, plan P5): an update is a checkout in each submodule, so the unsaved files in a
	// submodule's directory block it; a file elsewhere, or one whose name only starts like the path, does not.
	it('blocks the files in or below a submodule and nothing else', () => {
		const source = { dirtyPaths: () => ['lib/a.kt', 'lib/nested/b.kt', 'lib', 'libx/c.kt', 'app/d.kt', 'extra/e.kt'] };
		expect(dirtyUnder(source, ['lib'])).toEqual(['lib/a.kt', 'lib/nested/b.kt', 'lib']);
		expect(dirtyUnder(source, ['lib', 'extra'])).toEqual(['lib/a.kt', 'lib/nested/b.kt', 'lib', 'extra/e.kt']);
		expect(dirtyUnder(source, ['lib/nested'])).toEqual(['lib/nested/b.kt']);
		expect(dirtyUnder(source, [])).toEqual([]);
		expect(updateBlockText(['a'])).toMatch(/this file/);
		expect(updateBlockText(['a', 'b'])).toMatch(/these files/);
	});
});

describe('SubmoduleUpdateModel', () => {
	// Decision: one git.updateSubmodules per update, on the project's trimmed root: a row's with its path, the
	// section's with none (the daemon lists them afresh); the submodules are read again when it has ended.
	it('calls git.updateSubmodules with the row path or with none, and reads the submodules again', async () => {
		const f = fake();
		let done = 0;
		const m = model(f, [], () => done++);
		const one = m.update('lib');
		expect(f.calls).toEqual([{ name: 'git.updateSubmodules', params: { paths: ['lib'] }, workspace: '/p' }]);
		expect(m.view).toMatchObject({ state: 'running', path: 'lib', all: false, busy: true });
		f.ok(['lib']);
		await one;
		expect(m.view).toMatchObject({ state: 'idle', busy: false, failure: null });
		expect(done).toBe(1);

		const all = m.updateAll(['extra', 'lib']);
		expect(f.calls.at(-1)).toEqual({ name: 'git.updateSubmodules', params: {}, workspace: '/p' });
		expect(m.view).toMatchObject({ state: 'running', path: null, all: true });
		f.ok(['extra', 'lib']);
		await all;
		expect(done).toBe(2);
	});

	// Decision (ADR-0071 §5): with an unsaved buffer under the submodules the update touches, nothing is called
	// and the files are kept to be named; a buffer outside every submodule does not stop it.
	it('makes no call while a buffer under the paths is dirty, and names the files', async () => {
		const f = fake();
		let done = 0;
		const m = model(f, ['lib/a.kt', 'app/b.kt'], () => done++);
		await m.update('lib');
		await m.updateAll(['lib', 'extra']);
		expect(f.calls).toEqual([]);
		expect(m.view.blocked).toEqual(['lib/a.kt']);
		expect(m.view.blockedText).toMatch(/this file/);
		expect(m.view).toMatchObject({ state: 'idle', busy: false, all: true });
		expect(done, 'a refused update ends nothing to read again').toBe(0);

		// Another submodule's row is not stopped by the buffer in lib; the block is forgotten.
		const other = m.update('extra');
		expect(m.view.blocked).toEqual([]);
		expect(f.names()).toEqual(['git.updateSubmodules']);
		f.ok();
		await other;
	});

	// Decision: Dismiss forgets a block.
	it('forgets a block on dismiss', async () => {
		const m = model(fake(), ['lib/a.kt']);
		await m.update('lib');
		expect(m.view.blocked).toEqual(['lib/a.kt']);
		m.dismiss();
		expect(m.view.blocked).toEqual([]);
		expect(m.view.blockedText).toBe('');
	});

	// Decision (ADR-0071 §4): one write at a time; a second click while one is in flight makes no call.
	it('ignores an update asked for while one is in flight', async () => {
		const f = fake();
		const m = model(f);
		const first = m.update('lib');
		void m.update('extra');
		void m.updateAll(['lib', 'extra']);
		expect(f.names()).toEqual(['git.updateSubmodules']);
		f.ok();
		await first;
	});

	// Decision: shown queued when the daemon holds it behind another write on the repository, running otherwise,
	// following the activity feed; the feed is listened to only while it matters.
	it('is queued or running as the activity feed says, and stops listening at the end', async () => {
		const f = fake();
		const act = activity();
		const m = model(f, [], () => {}, act.a);
		const run = m.update('lib');
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

	// Decision: Cancel is operation.cancel on the operation this update started; a cancelled update is not a
	// failure, but it may have moved some submodules, so they are read again.
	it('cancels its own operation, shows no failure and reads the submodules again', async () => {
		const f = fake();
		let done = 0;
		const m = model(f, [], () => done++);
		const run = m.updateAll(['lib']);
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
		expect(done).toBe(1);
	});

	// Decision: a cancel pressed before the operation's id is known is sent when it arrives.
	it('sends a cancel asked for before the operation id is known when it arrives', async () => {
		const f = fake();
		const m = model(f);
		const run = m.update('lib');
		m.cancel();
		await settle();
		expect(f.names()).toEqual(['git.updateSubmodules']);
		f.started('op-late');
		await settle();
		expect(f.calls.at(-1)).toMatchObject({ name: 'operation.cancel', params: { operationId: 'op-late' } });
		f.fail(new CapabilityError({ code: 'OPERATION_CANCELLED', message: 'cancelled' }, false));
		await run;
	});

	// Decision (ADR-0071 §2): an untrusted root is a refusal the user reads, with its next step; nothing stops
	// the next click from calling again, and Dismiss forgets it.
	it('keeps a refusal with its message and next step, and lets the next click call again', async () => {
		const f = fake();
		const m = model(f);
		const run = m.update('lib');
		f.fail(refused());
		await run;
		expect(m.view.state).toBe('failed');
		expect(m.view.busy).toBe(false);
		expect(m.view.failure).toEqual({ code: 'WORKSPACE_UNTRUSTED', message: 'the project root "/p" is not trusted', nextStep: "ask the user to run `droidyard trust '/p'`" });
		const again = m.update('extra');
		expect(f.names()).toEqual(['git.updateSubmodules', 'git.updateSubmodules']);
		expect(m.view.failure).toBeNull();
		f.ok();
		await again;
		const bad = m.updateAll(['lib']);
		f.fail(refused());
		await bad;
		m.dismiss();
		expect(m.view).toMatchObject({ state: 'idle', failure: null });
	});

	// Decision (ADR-0071 §5): git's own words, the cause, are kept for the failure to show.
	it('keeps the cause of a failed update', async () => {
		const f = fake();
		const m = model(f);
		const run = m.update('lib');
		f.fail(new CapabilityError({ code: 'GIT_FAILED', message: 'git failed', cause: 'error: Your local changes would be overwritten:\n\tlib.txt', nextStep: 'fix it, then update again' }, false));
		await run;
		expect(m.view.failure).toEqual({ code: 'GIT_FAILED', message: 'git failed', cause: 'error: Your local changes would be overwritten:\n\tlib.txt', nextStep: 'fix it, then update again' });
	});
});

describe('the Update controls', () => {
	const R = 'a'.repeat(40);
	const LIST: Submodule[] = [
		{ path: 'extra', recorded: R, state: 'uninitialised' },
		{ path: 'lib', recorded: R, checkedOut: R, state: 'clean' },
		{ path: 'lib/nested', recorded: R, checkedOut: 'b'.repeat(40), state: 'outdated' },
	];
	const tag = (body: string, attr: string) => new RegExp(`<button[^>]*${attr}[^>]*>`).exec(body)?.[0] ?? '';

	async function panel(updater: SubmoduleUpdateModel, list: Submodule[] = LIST, truncated = false) {
		vi.stubGlobal('window', {});
		const t = setup(() => chain(3), 10);
		await settle();
		t.state.refs = refsOf(['c0']);
		t.store.refreshRefs();
		await settle();
		const submodules = await submodulesOf(list, truncated);
		const fetcher = new FetchModel({ call: (() => Promise.resolve({})) as FetchClient['call'] }, { root: '/p' }, () => {});
		return render(HistoryPanel, { props: { store: t.store, watch: t.watch as unknown as GitWatch, workspaceId: 'ws1', fetcher, branches: idleBranches(), submodules, updater } }).body;
	}

	// Decision (plan P5's hooks): the section has its Update and each row its own, enabled and idle, with no
	// Cancel; with no submodule there is nothing to update.
	it('renders the section Update and one per row, idle, with no Cancel', async () => {
		const body = await panel(model(fake()));
		expect(tag(body, 'data-submodules-update="all"')).not.toMatch(/\sdisabled/);
		expect(tag(body, 'data-submodules-update="all"')).toContain('data-op-state="idle"');
		for (const p of ['extra', 'lib', 'lib/nested']) {
			expect(tag(body, `data-submodule-update="${p}"`), p).toContain('data-op-state="idle"');
			expect(tag(body, `data-submodule-update="${p}"`), p).not.toMatch(/\sdisabled/);
		}
		expect(body).not.toContain('data-op-cancel');
		expect(body).not.toContain('data-dirty-block');
		expect(body).not.toContain('data-op-error');
		expect(await panel(model(fake()), [])).not.toContain('data-submodules-update');
	});

	// Decision (verifier round): when the list is cut, the section's Update says it updates the listed ones only.
	it('notes beside Update that only the listed submodules are updated when the list is cut', async () => {
		expect(await panel(model(fake()))).not.toContain('data-update-truncated');
		const body = await panel(model(fake()), LIST, true);
		expect(body).toContain('data-update-truncated');
		expect(tag(body, 'data-submodules-update="all"')).toContain('the listed submodules');
	});

	// Decision: while the section's update runs, every Update is disabled and the section's shows its state with
	// a Cancel; while a row's runs, that row carries the state and the Cancel, and the section's stays idle.
	it('shows the update in flight on its own button with Cancel, and disables the others', async () => {
		const all = model(fake());
		void all.updateAll(['extra', 'lib', 'lib/nested']);
		let body = await panel(all);
		expect(tag(body, 'data-submodules-update="all"')).toContain('data-op-state="running"');
		expect(tag(body, 'data-submodules-update="all"')).toMatch(/\sdisabled/);
		expect(tag(body, 'data-submodule-update="lib"')).toMatch(/\sdisabled/);
		expect(tag(body, 'data-submodule-update="lib"')).toContain('data-op-state="idle"');
		expect([...body.matchAll(/data-op-cancel/g)]).toHaveLength(1);

		const f = fake();
		const row = model(f);
		void row.update('lib/nested');
		body = await panel(row);
		expect(tag(body, 'data-submodule-update="lib/nested"')).toContain('data-op-state="running"');
		expect(tag(body, 'data-submodule-update="lib"')).toContain('data-op-state="idle"');
		expect(tag(body, 'data-submodules-update="all"')).toContain('data-op-state="idle"');
		expect(tag(body, 'data-submodules-update="all"')).toMatch(/\sdisabled/);
		expect([...body.matchAll(/data-op-cancel/g)]).toHaveLength(1);
		expect(body.indexOf('data-op-cancel')).toBeGreaterThan(body.indexOf('data-submodule-update="lib/nested"'));
	});

	// Decision (ADR-0071 §5): the refusal of the gate names the files, inside the section, and calls nothing.
	it('names the files of the dirty-buffer block, as text', async () => {
		const f = fake();
		const m = new SubmoduleUpdateModel(f.client, { root: '/p' }, { dirtyPaths: () => ['lib/<img src=x>.kt', 'lib/b.kt'] }, () => {});
		await m.update('lib');
		const body = await panel(m);
		expect(body).toMatch(/data-dirty-block[\s\S]*?these files[\s\S]*?lib\/&lt;img src=x>\.kt[\s\S]*?lib\/b\.kt/);
		expect(body).not.toContain('<img');
		expect(f.calls).toEqual([]);
	});

	// Decision (ADR-0071 §2, §5): a failure shows the code, the message, git's words as the cause and the next
	// step, all as text; the buttons stay enabled so another click calls again.
	it('shows a failure with its cause and next step, as text', async () => {
		const f = fake();
		const m = model(f);
		const run = m.update('lib');
		f.fail(new CapabilityError({ code: 'GIT_FAILED', message: 'git failed', cause: 'fatal: <script>alert(1)</script>', nextStep: 'run `git submodule update` in a terminal' }, false));
		await run;
		const body = await panel(m);
		expect(body).toMatch(/data-op-error(="")? data-op-of="submodules"[\s\S]*?GIT_FAILED: git failed[\s\S]*?&lt;script>alert\(1\)&lt;\/script>[\s\S]*?run `git submodule update` in a terminal/);
		expect(body).not.toContain('<script>');
		expect(tag(body, 'data-submodule-update="lib"')).toContain('data-op-state="failed"');
		expect(tag(body, 'data-submodule-update="lib"')).not.toMatch(/\sdisabled/);
	});

	// Decision (R1): after a run the section says what it did, as text, with a Dismiss; the button stays available.
	it('shows the summary of the last run, dismissible', async () => {
		const f = fake();
		let list: readonly Submodule[] = LIST;
		const m = new SubmoduleUpdateModel(f.client, { root: '/p' }, { dirtyPaths: () => [] }, () => void (list = [...LIST]), null, () => list);
		const run = m.updateAll(['lib']);
		f.ok();
		await run;
		const body = await panel(m);
		expect(body).toMatch(/data-update-summary[\s\S]*?All 2 submodules already at the recorded commits/);
		expect(tag(body, 'data-submodules-update="all"')).not.toMatch(/\sdisabled/);
		m.dismiss();
		expect(await panel(m)).not.toContain('data-update-summary');
	});

	// Decision: a path is git's text; a markup-looking one is escaped in the Update's label too.
	it('renders a hostile path in an Update label as text', async () => {
		const bad = '<img src=x onerror=alert(1)>';
		const body = await panel(model(fake()), [{ path: bad, recorded: R, state: 'clean', checkedOut: R }]);
		expect(body).not.toContain('<img');
	});
});

describe('what an update says it did (R1)', () => {
	const sm = (path: string, checkedOut: string | undefined, state: Submodule['state'] = 'clean'): Submodule => ({ path, recorded: 'r'.repeat(40), ...(checkedOut ? { checkedOut } : {}), state });
	const o = (c: string) => c.repeat(40);

	// A list the test replaces the way a read of the submodules does: with a new array.
	function held(first: Submodule[]) {
		let list: readonly Submodule[] = first;
		return { get: () => list, read: (next: Submodule[]) => (list = next) };
	}
	function withList(f: ReturnType<typeof fake>, h: ReturnType<typeof held>, after: Submodule[] | null) {
		return new SubmoduleUpdateModel(f.client, { root: '/p' }, { dirtyPaths: () => [] }, async () => void (after !== null && h.read([...after])), null, h.get);
	}

	// Decision (R1): when no checked-out commit changed, the section says everything was already at the recorded commits, with the count.
	it('says that all were already at the recorded commits when nothing moved', async () => {
		const f = fake();
		const h = held([sm('a', o('1')), sm('b', o('2')), sm('c', undefined, 'uninitialised')]);
		const m = withList(f, h, [sm('a', o('1')), sm('b', o('2')), sm('c', undefined, 'uninitialised')]);
		const run = m.updateAll(['a', 'b']);
		f.ok();
		await run;
		expect(m.view.summary).toEqual({ scope: null, total: 2, moved: [] });
		expect(summaryText(m.view.summary!)).toBe('All 2 submodules already at the recorded commits');
	});

	// Decision (R1): the ones that moved are named with the short commits before and after; the rest are not.
	it('names the submodules that moved, with the commits before and after', async () => {
		const f = fake();
		const h = held([sm('a', o('1')), sm('b', o('2'), 'outdated'), sm('c', undefined, 'uninitialised')]);
		const m = withList(f, h, [sm('a', o('1')), sm('b', o('3')), sm('c', o('4'))]);
		const run = m.updateAll([]);
		f.ok();
		await run;
		expect(m.view.summary?.moved).toEqual([
			{ path: 'b', from: '2222222', to: '3333333' },
			{ path: 'c', to: '4444444' },
		]);
		expect(summaryText(m.view.summary!, (p) => p.toUpperCase())).toBe('Updated 2 submodules: B (2222222 → 3333333), C (now 4444444)');
	});

	// Decision (R1): one row's update says that submodule is already there, by its display name.
	it('says a single submodule was already at the recorded commit', async () => {
		const f = fake();
		const h = held([sm('a', o('1'))]);
		const m = withList(f, h, [sm('a', o('1'))]);
		const run = m.update('a');
		f.ok();
		await run;
		expect(summaryText(m.view.summary!, () => 'lib')).toBe('lib is already at the recorded commit');
	});

	// Decision (R1): nothing false is said: a failed update has its failure and no summary, and a read that did not happen no summary.
	it('says nothing after a failure, a read that did not happen, or a cancel that moved nothing', async () => {
		const f = fake();
		const h = held([sm('a', o('1'))]);
		const failing = withList(f, h, [sm('a', o('9'))]);
		const bad = failing.update('a');
		f.fail(refused());
		await bad;
		expect(failing.view.summary).toBeNull();
		expect(failing.view.failure).not.toBeNull();

		const f2 = fake();
		const unread = withList(f2, h, null);
		const run = unread.update('a');
		f2.ok();
		await run;
		expect(unread.view.summary).toBeNull();

		const f3 = fake();
		const cancelled = withList(f3, held([sm('a', o('1'))]), [sm('a', o('1'))]);
		const c = cancelled.update('a');
		f3.started('op');
		cancelled.cancel();
		f3.fail(new CapabilityError({ code: 'OPERATION_CANCELLED', message: 'cancelled' }, false));
		await c;
		expect(cancelled.view.summary).toBeNull();
	});

	// Decision (R1): the summary is dismissible, and the next run clears it.
	it('is forgotten on dismiss and by the next run', async () => {
		const f = fake();
		const h = held([sm('a', o('1'))]);
		const m = withList(f, h, [sm('a', o('1'))]);
		let run = m.update('a');
		f.ok();
		await run;
		expect(m.view.summary).not.toBeNull();
		m.dismiss();
		expect(m.view.summary).toBeNull();
		run = m.update('a');
		f.ok();
		await run;
		expect(m.view.summary).not.toBeNull();
		const next = m.update('a');
		expect(m.view.summary).toBeNull();
		f.ok();
		await next;
	});

	it('words the summaries', () => {
		expect(summaryText({ scope: null, total: 0, moved: [] })).toBe('No submodule is checked out; nothing to update');
		expect(summaryText({ scope: null, total: 1, moved: [] })).toBe('The only submodule is already at the recorded commit');
		const many = Array.from({ length: 8 }, (_, i) => ({ path: `m${i}`, from: 'aaaaaaa', to: 'bbbbbbb' }));
		expect(summaryText({ scope: null, total: 8, moved: many })).toMatch(/^Updated 8 submodules: m0 \(aaaaaaa → bbbbbbb\), .*m5 \(aaaaaaa → bbbbbbb\) and 2 more$/);
	});
});
