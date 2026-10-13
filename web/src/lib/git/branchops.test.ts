import { describe, expect, it } from 'vitest';
import type { Params, Result } from '../api/capabilities.gen';
import { CapabilityError, type CallOptions } from '../api/client';
import { BranchModel, type BranchClient } from './branch';
import { blockText, checkoutParams, createParams, dirtyBlock, entryAction, headSource, isRemoteHead, nameProblem, sourceOf, targetLabel, type BranchTarget } from './branchops';
import { BRANCH_MENU, type MenuId } from './controls';

// Checkout and Create Branch (plan m2n P3; ADR-0071 §1, §5): what each enabled entry calls, the
// dirty-buffer gate, and the model that makes the call.

const local: BranchTarget = { kind: 'local', name: 'feature/x', oid: 'a'.repeat(40) };
const remote: BranchTarget = { kind: 'remote', remote: 'origin', name: 'feature/y', oid: 'b'.repeat(40) };

interface Call {
	name: string;
	params: unknown;
	workspace: string | undefined;
}

/** A client that records every call and answers with `answer` (a result, or an error to throw). */
function fake(answer: () => unknown = () => ({ outcomes: [{ root: '.', status: 'done' }] })) {
	const calls: Call[] = [];
	let release: (() => void) | null = null;
	let hold = false;
	const client: BranchClient = {
		call: (async (name: string, params: unknown, opts?: CallOptions) => {
			calls.push({ name, params, workspace: opts?.workspace });
			if (hold) await new Promise<void>((r) => (release = r));
			const a = answer();
			if (a instanceof Error) throw a;
			return a;
		}) as BranchClient['call'],
	};
	return {
		calls,
		client,
		hold() {
			hold = true;
		},
		release() {
			hold = false;
			release?.();
		},
	};
}

const model = (f: ReturnType<typeof fake>, dirty: string[] = [], onDone: () => void = () => {}) =>
	new BranchModel(f.client, { root: ' /p ' }, { dirtyPaths: () => dirty }, onDone);

describe('what the entries call', () => {
	// Decision (ADR-0071 §1): Checkout of a local branch is its name alone; of a remote-tracking branch, its name on the remote and the remote, so the daemon creates the tracking branch (or switches to the local one of that name).
	it('builds git.checkout for a local and for a remote-tracking branch', () => {
		expect(checkoutParams(local)).toEqual({ name: 'feature/x' });
		expect(checkoutParams(remote)).toEqual({ name: 'feature/y', remote: 'origin' });
		expect(entryAction('checkout', remote)).toEqual({ kind: 'checkout', target: remote, params: { name: 'feature/y', remote: 'origin' } });
	});

	// Decision (plan P3): New branch from a remote-tracking branch proposes its name without the remote; from a local one, or from HEAD, it proposes nothing (the name is taken).
	it('proposes the remote-tracking branch name without the remote', () => {
		expect(sourceOf(remote)).toEqual({ label: 'origin/feature/y', oid: 'b'.repeat(40), proposed: 'feature/y' });
		expect(sourceOf(local)).toEqual({ label: 'feature/x', oid: 'a'.repeat(40), proposed: '' });
		// A remote named with a slash keeps the whole remote out of the proposal.
		expect(sourceOf({ kind: 'remote', remote: 'team/up', name: 'dev', oid: 'c' })?.proposed).toBe('dev');
		expect(entryAction('new-branch-from', remote)).toEqual({ kind: 'new-branch', source: sourceOf(remote) });
		expect(targetLabel(remote)).toBe('origin/feature/y');
	});

	// Decision: the start is the branch's commit, which a ref may lack; then the entry opens nothing.
	it('opens no dialog for a ref with no commit', () => {
		expect(entryAction('new-branch-from', { kind: 'local', name: 'x' })).toBeNull();
		expect(sourceOf({ kind: 'local', name: 'x', oid: '' })).toBeNull();
	});

	// Decision (plan P3): the toolbar's Create Branch starts at HEAD, with its short oid shown by the dialog; it has nothing to start from before the refs arrive or in a repository with no commit.
	it('starts the toolbar Create Branch at HEAD', () => {
		const head = { oid: 'd'.repeat(40), branch: 'main', detached: false, upstreamGone: false, ahead: 0, behind: 0 };
		expect(headSource(head)).toEqual({ label: 'HEAD (main)', oid: 'd'.repeat(40), proposed: '' });
		expect(headSource({ ...head, branch: undefined, detached: true })?.label).toBe('HEAD (detached)');
		expect(headSource(null)).toBeNull();
		expect(headSource({ ...head, oid: undefined })).toBeNull();
	});

	// Decision (verifier): a remote's HEAD pointer is not a branch; no entry does anything for it, whatever the list offers.
	it('offers nothing for a remote’s HEAD', () => {
		const head: BranchTarget = { kind: 'remote', remote: 'origin', name: 'HEAD', oid: 'c'.repeat(40) };
		expect(isRemoteHead(head)).toBe(true);
		expect(isRemoteHead(remote)).toBe(false);
		expect(entryAction('checkout', head)).toBeNull();
		expect(entryAction('new-branch-from', head)).toBeNull();
	});

	// Decision (plan P3, items 7 and 8): the other entries of the menu do nothing here, whatever the branch: Compare with current waits for step P9, the rest for a later milestone.
	it('calls nothing for the other entries of the menu', () => {
		const others = BRANCH_MENU.map((c) => c.id).filter((id) => id !== 'checkout' && id !== 'new-branch-from');
		expect(others).toEqual(['compare', 'merge', 'rebase', 'push', 'rename', 'delete']);
		for (const id of others) {
			expect(entryAction(id as MenuId, local), id).toBeNull();
			expect(entryAction(id as MenuId, remote), id).toBeNull();
		}
		expect(BRANCH_MENU.filter((c) => c.enabled).map((c) => c.id)).toEqual(['checkout', 'new-branch-from']);
	});

	// Decision: the createBranch parameters carry the three required fields and no roots (the UI sends the default root until step P8).
	it('builds git.createBranch with name, checkout and from', () => {
		expect(createParams('n', true, 'e'.repeat(40))).toEqual({ name: 'n', checkout: true, from: 'e'.repeat(40) });
		expect(createParams('n', false, 'e'.repeat(40))).toEqual({ name: 'n', checkout: false, from: 'e'.repeat(40) });
	});

	// Decision: a name the list already holds, or none, is refused in the dialog before the call; anything else is the daemon's to judge.
	it('refuses an empty name and an existing branch before the call', () => {
		const locals = new Set(['main', 'feature/x']);
		expect(nameProblem('', locals)).toMatch(/name/);
		expect(nameProblem('   ', locals)).toMatch(/name/);
		expect(nameProblem('main', locals)).toMatch(/already exists/);
		expect(nameProblem(' feature/x ', locals)).toMatch(/already exists/);
		expect(nameProblem('feature/z', locals)).toBeNull();
		expect(nameProblem('-b', locals)).toBeNull();
	});
});

describe('the dirty-buffer gate', () => {
	// Decision (ADR-0071 §5): a checkout, and a new branch that is checked out, rewrite the work tree under the buffers; a new branch that is not checked out touches no file.
	it('blocks what rewrites the work tree and nothing else', () => {
		const source = { dirtyPaths: () => ['a.kt', 'b/c.kt'] };
		expect(dirtyBlock(source, true)).toEqual(['a.kt', 'b/c.kt']);
		expect(dirtyBlock(source, false)).toEqual([]);
		expect(dirtyBlock({ dirtyPaths: () => [] }, true)).toEqual([]);
		expect(blockText(['a.kt'])).toMatch(/this file/);
		expect(blockText(['a.kt', 'b'])).toMatch(/these files/);
	});

	// Decision (ADR-0071 §5): with a dirty buffer, Checkout names the files and calls nothing, for a local and for a remote-tracking branch; the files are kept for the message.
	it('makes no call for a checkout while a buffer is dirty, and names the files', async () => {
		const f = fake();
		const m = model(f, ['a.kt', 'src/b.kt']);
		expect(await m.checkout(local)).toBe(false);
		expect(await m.checkout(remote)).toBe(false);
		expect(f.calls).toEqual([]);
		expect(m.view.blocked).toEqual(['a.kt', 'src/b.kt']);
		expect(m.view.blockedText).toMatch(/these files/);
		expect(m.view.failure).toBeNull();
		m.dismiss();
		expect(m.view.blocked).toEqual([]);
	});

	// Decision (ADR-0071 §5): a new branch with checkout is blocked the same way; one without is not blocked and makes its call.
	it('blocks New branch with checkout and lets New branch without it through', async () => {
		const f = fake();
		const dirty = ['a.kt'];
		const m = model(f, dirty);
		expect(await m.createBranch('n', true, 'a'.repeat(40))).toBe(false);
		expect(f.calls).toEqual([]);
		expect(m.view.blocked).toEqual(['a.kt']);
		expect(m.dirtyFor(true)).toEqual(['a.kt']);
		expect(m.dirtyFor(false)).toEqual([]);

		expect(await m.createBranch('n', false, 'a'.repeat(40))).toBe(true);
		expect(f.calls).toEqual([{ name: 'git.createBranch', params: { name: 'n', checkout: false, from: 'a'.repeat(40) }, workspace: '/p' }]);
		expect(m.view.blocked).toEqual([]);
	});

	// Decision: the gate reads the buffers at the moment of the call, so files saved meanwhile no longer block.
	it('asks the editor again at each call', async () => {
		const f = fake();
		const dirty = ['a.kt'];
		const m = model(f, dirty);
		expect(await m.checkout(local)).toBe(false);
		dirty.length = 0;
		expect(await m.checkout(local)).toBe(true);
		expect(f.calls).toHaveLength(1);
		expect(m.view.blocked).toEqual([]);
	});
});

describe('the model', () => {
	// Decision (ADR-0071 §1): one call per click, in the project's root; a done outcome is a success, the refs are read again, nothing is left to show.
	it('calls git.checkout once and reloads the refs on a done outcome', async () => {
		const f = fake();
		let reloads = 0;
		const m = model(f, [], () => reloads++);
		expect(await m.checkout(remote)).toBe(true);
		expect(f.calls).toEqual([{ name: 'git.checkout', params: { name: 'feature/y', remote: 'origin' }, workspace: '/p' }]);
		expect(reloads).toBe(1);
		expect(m.view).toMatchObject({ busy: false, failure: null, blocked: [] });
	});

	// Decision (ADR-0071 §5): a refused checkout is a failed outcome of the call, not an error: git's own words (cause) and next step reach the view as text, and the refs are read again all the same.
	it('shows a failed outcome with git’s cause and reloads the refs', async () => {
		const outcome = { root: '.', status: 'failed', error: { code: 'GIT_FAILED', message: 'git refused the checkout', cause: 'error: Your local changes would be overwritten by checkout:\n\tshared.txt', nextStep: 'commit or stash them' } };
		const f = fake(() => ({ outcomes: [outcome] }));
		let reloads = 0;
		const m = model(f, [], () => reloads++);
		expect(await m.checkout(local)).toBe(false);
		expect(m.view.failure).toEqual({ code: 'GIT_FAILED', message: 'git refused the checkout', cause: outcome.error.cause, nextStep: 'commit or stash them' });
		expect(reloads).toBe(1);
		m.dismiss();
		expect(m.view.failure).toBeNull();
	});

	// Decision (plan P3): a call the daemon refuses as a whole (a name, an oid) is a failure with its code and next step, and the refs are read again too.
	it('keeps the error of a refused call and reloads the refs', async () => {
		const f = fake(() => new CapabilityError({ code: 'REQUEST_INVALID_PARAMS', message: 'a branch named "x" already exists', nextStep: 'choose another name' }, false));
		let reloads = 0;
		const m = model(f, [], () => reloads++);
		expect(await m.createBranch('x', false, 'a'.repeat(40))).toBe(false);
		expect(m.view.failure).toEqual({ code: 'REQUEST_INVALID_PARAMS', message: 'a branch named "x" already exists', nextStep: 'choose another name' });
		expect(reloads).toBe(1);
	});

	// Decision: a cancelled write is the root's failed outcome with its next step, shown like any other.
	it('shows a cancelled write with its next step', async () => {
		const f = fake(() => ({ outcomes: [{ root: '.', status: 'failed', error: { code: 'OPERATION_CANCELLED', message: 'the operation was cancelled while git was writing', nextStep: 'run `git status` in a terminal' } }] }));
		const m = model(f);
		await m.checkout(local);
		expect(m.view.failure).toMatchObject({ code: 'OPERATION_CANCELLED', nextStep: 'run `git status` in a terminal' });
	});

	// Decision (ADR-0071 §4): one write at a time from this tab; a click while a call is in flight makes none.
	it('ignores a call asked for while one is in flight', async () => {
		const f = fake();
		f.hold();
		const m = model(f);
		const first = m.checkout(local);
		expect(m.view.busy).toBe(true);
		expect(await m.checkout(remote)).toBe(false);
		expect(await m.createBranch('n', false, 'a'.repeat(40))).toBe(false);
		expect(f.calls).toHaveLength(1);
		f.release();
		expect(await first).toBe(true);
		expect(m.view.busy).toBe(false);
	});

	// Decision: an answer with no outcome, or a skipped one, is never taken for success.
	it('does not take an empty answer for a success', async () => {
		const m = model(fake(() => ({ outcomes: [] })));
		expect(await m.checkout(local)).toBe(false);
		expect(m.view.failure?.message).toMatch(/no outcome/);
	});

	// Decision: a model that went away with its project makes no call and reloads nothing.
	it('does nothing once disposed', async () => {
		const f = fake();
		let reloads = 0;
		const m = model(f, [], () => reloads++);
		m.dispose();
		expect(await m.checkout(local)).toBe(false);
		expect(f.calls).toEqual([]);
		expect(reloads).toBe(0);
	});
});

// Types the file relies on: the generated client accepts exactly these parameters and results.
const _params: Params<'git.checkout'> = { name: 'x', remote: 'origin', roots: ['.'] };
const _result: Result<'git.createBranch'> = { outcomes: [{ root: '.', status: 'skipped' }] };
void _params;
void _result;
