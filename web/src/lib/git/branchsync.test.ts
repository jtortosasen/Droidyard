import { render } from 'svelte/server';
import { describe, expect, it } from 'vitest';
import type { Result } from '../api/capabilities.gen';
import { CapabilityError, type CallOptions } from '../api/client';
import { BranchModel, type BranchClient } from './branch';
import { callRoots, checkoutOffer, checkoutParams, createParams, headSource, newBranchOffer, sourceOf, syncOn, type BranchTarget } from './branchops';
import CheckoutDialog from './CheckoutDialog.svelte';
import CreateBranchDialog from './CreateBranchDialog.svelte';
import { refsOf } from './historytesting';

// Synchronised branches (plan m2n P8, R2; ADR-0071 §6 as ADR-0074 §3 changed it): which roots have the branch (they move) and
// which do not (they keep theirs), the toggle’s default, the `roots` of the call, each root’s start, and the outcomes listed per root.

const A = 'a'.repeat(40);
const LIB = 'b'.repeat(40);
const NESTED = 'c'.repeat(40);
const OID = 'd'.repeat(40);

type Refs = Result<'git.refs'>;
const roots = ['lib', 'lib/nested'];
const tag = (body: string, attr: string, el: string) => new RegExp(`<${el}[^>]*${attr}[^>]*>`).exec(body)?.[0] ?? '';

/** A root's refs with the given local branches (name -> oid) and remote-tracking ones ("origin/name" -> oid). */
function refsWith(locals: Record<string, string>, remotes: Record<string, string> = {}, over: Partial<Refs> = {}): Refs {
	return refsOf([OID], {
		branches: Object.entries(locals).map(([name, oid]) => ({ name, oid })),
		remotes: Object.entries(remotes).map(([ref, oid]) => ({ remote: ref.split('/')[0], name: ref.split('/').slice(1).join('/'), oid })),
		...over,
	});
}

const ws = refsWith({ main: OID, 'feature/x': A });
const wsLacks = refsWith({ main: OID });
const both = new Map<string, Refs>([
	['lib', refsWith({ main: OID, 'feature/x': LIB })],
	['lib/nested', refsWith({ main: OID, 'feature/x': NESTED })],
]);
const nestedLacks = new Map<string, Refs>([
	['lib', refsWith({ main: OID, 'feature/x': LIB })],
	['lib/nested', refsWith({ main: OID })],
]);
const local: BranchTarget = { kind: 'local', name: 'feature/x', oid: A };

describe('which roots have the branch, and the toggle’s default', () => {
	// Decision (ADR-0074 §3): the call moves the roots that have the branch; a root that lacks it keeps its branch and is listed as staying. The toggle starts on whenever a submodule has it.
	it('moves the roots that have the branch and leaves the others', () => {
		const all = checkoutOffer(local, roots, both, ws)!;
		expect(all.moves).toEqual(['lib', 'lib/nested']);
		expect(all.stays).toEqual([]);
		expect(all.defaultOn).toBe(true);
		expect(all.workspaceHas).toBe(true);
		expect(all.forced).toBe(false);

		const partial = checkoutOffer(local, roots, nestedLacks, ws)!;
		expect(partial.moves).toEqual(['lib']);
		expect(partial.stays).toEqual(['lib/nested']);
		expect(partial.defaultOn).toBe(true);

		// No submodule has it: nothing to synchronise, the toggle starts off and the call is the workspace's alone.
		const none = checkoutOffer(local, roots, new Map([['lib', refsWith({ main: OID })], ['lib/nested', refsWith({ main: OID })]]), ws)!;
		expect(none.moves).toEqual([]);
		expect(none.defaultOn).toBe(false);
		expect(checkoutOffer(local, [], both, ws)).toBeNull();
	});

	// Decision (ADR-0074 §3): the roots of the call are exactly the roots that have the branch — the workspace "." only when it has it, so a branch only a submodule has is checked out in that submodule alone.
	it('passes exactly the roots that have the branch', () => {
		const all = checkoutOffer(local, roots, both, ws)!;
		expect(callRoots(all, syncOn(null, all))).toEqual(['.', 'lib', 'lib/nested']);
		expect(callRoots(all, false)).toEqual(['.']);

		const partial = checkoutOffer(local, roots, nestedLacks, ws)!;
		expect(callRoots(partial, syncOn(null, partial))).toEqual(['.', 'lib']);

		// The workspace lacks the branch: the toggle is forced on (it cannot be turned off) and "." is not in the call.
		const only = checkoutOffer(local, roots, nestedLacks, wsLacks)!;
		expect(only.workspaceHas).toBe(false);
		expect(only.forced).toBe(true);
		expect(syncOn(null, only)).toBe(true);
		expect(syncOn(false, only)).toBe(true);
		expect(callRoots(only, syncOn(false, only))).toEqual(['lib']);
		// ... and with no root that has it, there is no call at all.
		const nowhere = checkoutOffer(local, roots, new Map([['lib', refsWith({ main: OID })]]), wsLacks)!;
		expect(callRoots(nowhere, syncOn(null, nowhere))).toEqual([]);
	});

	// Decision: a root whose refs are not read yet cannot be said to have the branch, so it keeps its branch for now and is listed as pending, not as lacking it; the workspace’s refs not being read is not a reason to leave it out.
	it('keeps a root whose refs are not read out of the call', () => {
		const offer = checkoutOffer(local, roots, new Map([['lib', refsWith({ 'feature/x': LIB })]]), ws)!;
		expect(offer.moves).toEqual(['lib']);
		expect(offer.pending).toEqual(['lib/nested']);
		expect(offer.stays).toEqual([]);
		const late = checkoutOffer(local, roots, new Map(), ws)!;
		expect(late.defaultOn).toBe(false);
		expect(late.pending).toEqual(roots);
		expect(checkoutOffer(local, roots, both, null)!.workspaceHas).toBe(true);
	});

	// Decision (ADR-0074 §3): for a remote-tracking target a root has the branch when it has that remote-tracking branch; a local branch of that name, or another remote's branch, does not count.
	it('needs the remote-tracking branch itself in each root for a remote-tracking checkout', () => {
		const remote: BranchTarget = { kind: 'remote', remote: 'origin', name: 'feature/y', oid: A };
		const wsRemote = refsWith({ main: OID }, { 'origin/feature/y': A });
		const tracking = new Map([
			['lib', refsWith({}, { 'origin/feature/y': LIB })],
			['lib/nested', refsWith({ 'feature/y': NESTED }, { 'origin/main': OID })],
		]);
		const offer = checkoutOffer(remote, roots, tracking, wsRemote)!;
		expect(offer.moves).toEqual(['lib']);
		expect(offer.stays).toEqual(['lib/nested']);
		const other = new Map([...tracking, ['lib', refsWith({}, { 'upstream/feature/y': LIB })]]);
		expect(checkoutOffer(remote, roots, other, wsRemote)?.moves).toEqual([]);
		// the workspace itself must have the remote-tracking branch for "." to be in the call
		const wsNo = refsWith({ main: OID });
		expect(callRoots(checkoutOffer(remote, roots, tracking, wsNo), true)).toEqual(['lib']);
		// a local target needs no remote
		expect(checkoutOffer({ kind: 'local', name: 'feature/y' }, roots, tracking, wsRemote)?.moves).toEqual(['lib/nested']);
	});

	// Decision: the toggle follows the offer until the person touches it, so refs that arrive after the dialog opened turn it on; once set, it stays as set; a forced toggle ignores the choice.
	it('follows the offer’s default until the toggle is touched', () => {
		const source = sourceOf(local)!;
		const early = newBranchOffer(source, roots, new Map([['lib', refsWith({ main: OID })]]), ws);
		const late = newBranchOffer(source, roots, both, ws);
		expect(syncOn(null, early)).toBe(false);
		expect(syncOn(null, late)).toBe(true);
		expect(syncOn(false, late)).toBe(false);
		expect(syncOn(true, early)).toBe(true);
		expect(syncOn(true, null)).toBe(false);
		expect(syncOn(null, null)).toBe(false);
		expect(callRoots(null, false)).toEqual(['.']);
	});

	// Decision (ADR-0074 §3, plan Q6 (1)): a new branch from a branch starts, in each root that has it, at that branch’s oid there; a root without it keeps its branch and has no start.
	it('takes each root’s start from the source branch there', () => {
		const source = sourceOf(local)!;
		const offer = newBranchOffer(source, roots, both, ws)!;
		expect(offer.defaultOn).toBe(true);
		expect([...offer.starts]).toEqual([
			['lib', LIB],
			['lib/nested', NESTED],
		]);
		const partial = newBranchOffer(source, roots, nestedLacks, ws)!;
		expect(partial.moves).toEqual(['lib']);
		expect(partial.stays).toEqual(['lib/nested']);
		expect([...partial.starts]).toEqual([['lib', LIB]]);
		// A remote-tracking source is looked for among the remote-tracking branches, not the local ones.
		const remoteSource = sourceOf({ kind: 'remote', remote: 'origin', name: 'feature/x', oid: A })!;
		expect(newBranchOffer(remoteSource, roots, both, ws)?.moves).toEqual([]);
		// A source only a submodule has: the workspace is not in the call.
		const away = sourceOf({ kind: 'local', name: 'feature/x', oid: LIB, onlyIn: 'lib' })!;
		const onlyLib = newBranchOffer(away, roots, nestedLacks, wsLacks)!;
		expect(onlyLib.forced).toBe(true);
		expect(callRoots(onlyLib, true)).toEqual(['lib']);
	});

	// Decision (ADR-0074 §3): the toolbar’s Create Branch is made in every initialised root, so no root stays: where a root has the current branch it starts there, else at that root’s own HEAD; the project always has it.
	it('makes the toolbar’s Create Branch in every initialised root', () => {
		const head = { oid: A, branch: 'feature/x', detached: false, upstreamGone: false, ahead: 0, behind: 0 };
		const heads = new Map<string, Refs>([
			['lib', refsWith({ main: OID, 'feature/x': LIB })],
			['lib/nested', refsWith({ main: OID }, {}, { head: { oid: NESTED, detached: true, upstreamGone: false, ahead: 0, behind: 0 } })],
		]);
		const offer = newBranchOffer(headSource(head)!, roots, heads, ws)!;
		expect(offer.stays).toEqual([]);
		expect(offer.moves).toEqual(roots);
		expect(offer.atHead).toEqual(['lib/nested']);
		expect([...offer.starts]).toEqual([
			['lib', LIB],
			['lib/nested', NESTED],
		]);
		expect(offer.forced).toBe(false);
		expect(callRoots(offer, syncOn(null, offer))).toEqual(['.', 'lib', 'lib/nested']);
		// A detached project HEAD has no branch to look for: each root starts at its own HEAD.
		const detached = newBranchOffer(headSource({ ...head, branch: undefined, detached: true })!, roots, heads, ws)!;
		expect(detached.branch).toBe('');
		expect(detached.moves).toEqual(roots);
		expect(detached.atHead).toEqual(roots);
	});
});

describe('the parameters of a synchronised call', () => {
	// Decision (ADR-0074 §3): `roots` is exactly what the offer says; the workspace alone sends none, and a submodule alone sends only it.
	it('sends the roots of the call to a checkout, none for the workspace alone', () => {
		expect(checkoutParams(local)).toEqual({ name: 'feature/x' });
		expect(checkoutParams(local, [])).toEqual({ name: 'feature/x' });
		expect(checkoutParams(local, ['.'])).toEqual({ name: 'feature/x' });
		expect(checkoutParams(local, ['.', 'lib', 'lib/nested'])).toEqual({ name: 'feature/x', roots: ['.', 'lib', 'lib/nested'] });
		expect(checkoutParams(local, ['lib'])).toEqual({ name: 'feature/x', roots: ['lib'] });
		expect(checkoutParams({ kind: 'remote', remote: 'origin', name: 'y' }, ['lib'])).toEqual({ name: 'y', remote: 'origin', roots: ['lib'] });
	});

	// Decision (plan Q6 (1)): each submodule’s start is its own oid in fromIn, "." is never in fromIn and `from` stays the source’s; the root without a start has no entry (the daemon refuses it).
	it('gives a new branch one start per submodule in fromIn', () => {
		const offer = newBranchOffer(sourceOf(local)!, roots, both, ws)!;
		const sync = { roots: callRoots(offer, true), starts: offer.starts };
		expect(createParams('n', true, A, sync)).toEqual({
			name: 'n',
			checkout: true,
			from: A,
			roots: ['.', 'lib', 'lib/nested'],
			fromIn: { lib: LIB, 'lib/nested': NESTED },
		});
		const partial = newBranchOffer(sourceOf(local)!, roots, nestedLacks, ws)!;
		const p = createParams('n', false, A, { roots: callRoots(partial, true), starts: partial.starts });
		expect(p.roots).toEqual(['.', 'lib']);
		expect(p.fromIn).toEqual({ lib: LIB });
		// only a submodule has the start: the workspace is not asked for
		const away = createParams('n', true, LIB, { roots: ['lib'], starts: new Map([['lib', LIB]]) });
		expect(away).toEqual({ name: 'n', checkout: true, from: LIB, roots: ['lib'], fromIn: { lib: LIB } });
		expect(createParams('n', false, A)).toEqual({ name: 'n', checkout: false, from: A });
		expect(createParams('n', false, A, { roots: ['.'], starts: new Map() })).toEqual({ name: 'n', checkout: false, from: A });
		expect(createParams('n', false, A, { roots: [], starts: new Map() })).toEqual({ name: 'n', checkout: false, from: A });
	});
});

interface Call {
	name: string;
	params: unknown;
}

function fakeClient(answer: unknown) {
	const calls: Call[] = [];
	const client: BranchClient = {
		call: (async (name: string, params: unknown, _opts?: CallOptions) => {
			calls.push({ name, params });
			if (answer instanceof Error) throw answer;
			return answer;
		}) as BranchClient['call'],
	};
	return { calls, model: new BranchModel(client, { root: '/p' }, { dirtyPaths: () => [] }, () => {}) };
}

const stopped = {
	outcomes: [
		{ root: '.', status: 'done' },
		{ root: 'lib', status: 'failed', error: { code: 'GIT_FAILED', message: 'git refused the checkout', cause: 'error: <img src=x onerror=1>\n\tlib.txt', nextStep: 'commit or put the changes aside' } },
		{ root: 'lib/nested', status: 'skipped' },
	],
};

describe('the model’s outcomes per root', () => {
	// Decision (ADR-0071 §6): a stopped synchronised call lists every root with its status in the order applied, git's words on the failed one only, and is not done; there is no single failure to show.
	it('lists a stopped call root by root', async () => {
		const f = fakeClient(stopped);
		expect(await f.model.checkout(local, ['.', ...roots])).toBe(false);
		expect(f.calls).toEqual([{ name: 'git.checkout', params: { name: 'feature/x', roots: ['.', 'lib', 'lib/nested'] } }]);
		const { outcomes, failure } = f.model.view;
		expect(outcomes.map((o) => `${o.root}:${o.status}`)).toEqual(['.:done', 'lib:failed', 'lib/nested:skipped']);
		expect(outcomes[1].failure).toEqual({ code: 'GIT_FAILED', message: 'git refused the checkout', cause: 'error: <img src=x onerror=1>\n\tlib.txt', nextStep: 'commit or put the changes aside' });
		expect(outcomes[0].failure).toBeUndefined();
		expect(outcomes[2].failure).toBeUndefined();
		expect(failure).toBeNull();
		f.model.dismiss();
		expect(f.model.view.outcomes).toEqual([]);
	});

	// Decision: a synchronised call is done when every root is, and a call of the project alone keeps P3's single-outcome view.
	it('is done when every root is, and keeps no list for one root', async () => {
		const all = fakeClient({ outcomes: ['.', 'lib'].map((root) => ({ root, status: 'done' })) });
		expect(await all.model.createBranch('n', true, A, { roots: ['.', 'lib'], starts: new Map([['lib', LIB]]) })).toBe(true);
		expect(all.calls[0].params).toEqual({ name: 'n', checkout: true, from: A, roots: ['.', 'lib'], fromIn: { lib: LIB } });
		expect(all.model.view.outcomes.map((o) => o.status)).toEqual(['done', 'done']);

		const one = fakeClient({ outcomes: [{ root: '.', status: 'failed', error: { code: 'GIT_FAILED', message: 'no', cause: 'error: x' } }] });
		expect(await one.model.checkout(local)).toBe(false);
		expect(one.model.view.outcomes).toEqual([]);
		expect(one.model.view.failure?.cause).toBe('error: x');
	});

	// Decision (ADR-0074 §3): a checkout of a branch only a submodule has calls with that root alone, and its outcome is listed under its own name, done or failed with git’s words, not folded into one failure.
	it('lists the outcome of a submodule that is called alone', async () => {
		const done = fakeClient({ outcomes: [{ root: 'lib', status: 'done' }] });
		expect(await done.model.checkout(local, ['lib'])).toBe(true);
		expect(done.calls).toEqual([{ name: 'git.checkout', params: { name: 'feature/x', roots: ['lib'] } }]);
		expect(done.model.view.outcomes.map((o) => `${o.root}:${o.status}`)).toEqual(['lib:done']);

		const failed = fakeClient({ outcomes: [{ root: 'lib', status: 'failed', error: { code: 'GIT_FAILED', message: 'git refused the checkout', cause: 'error: lib.txt' } }] });
		expect(await failed.model.checkout(local, ['lib'])).toBe(false);
		expect(failed.model.view.outcomes[0].failure?.cause).toBe('error: lib.txt');
		expect(failed.model.view.failure).toBeNull();
	});

	// Decision (ADR-0074 §3): the workspace alone sends no roots at all, whichever way it is asked.
	it('sends no roots for the workspace alone', async () => {
		const f = fakeClient({ outcomes: [{ root: '.', status: 'done' }] });
		expect(await f.model.checkout(local, ['.'])).toBe(true);
		expect(f.calls[0].params).toEqual({ name: 'feature/x' });
	});

	// Decision (ADR-0071 §6): a call refused whole (a root lacking the branch) applied nothing: the daemon's refusal is the failure and no root is listed.
	it('shows a refused call as a failure and no outcomes', async () => {
		const f = fakeClient(new CapabilityError({ code: 'REQUEST_INVALID_PARAMS', message: 'there is no local branch "x" (in the submodule lib)' }, false));
		expect(await f.model.checkout(local, ['.', ...roots])).toBe(false);
		expect(f.model.view.outcomes).toEqual([]);
		expect(f.model.view.failure?.message).toMatch(/submodule lib/);
	});
});

describe('the dialogs’ markup', () => {
	const model = (answer: unknown = stopped) => fakeClient(answer).model;
	const names = new Map([
		['.', 'my-app'],
		['lib', 'lib'],
		['lib/nested', 'nested'],
	]);
	const createOf = (offer: ReturnType<typeof newBranchOffer>, m = model()) =>
		render(CreateBranchDialog, { props: { source: { ...sourceOf(local)!, proposed: 'newb' }, locals: new Set(['main']), model: m, view: m.view, offer, names, onsubmit: async () => true, oncancel: () => {} } }).body;
	const checkoutOf = (offer: NonNullable<ReturnType<typeof checkoutOffer>>, m = model(), target = local) =>
		render(CheckoutDialog, { props: { target, offer, model: m, view: m.view, names, onsubmit: async () => true, oncancel: () => {} } }).body;
	const noneHave = new Map([
		['lib', refsWith({ main: OID })],
		['lib/nested', refsWith({ main: OID })],
	]);

	// Decision (ADR-0074 §3): the toggle is on in the New branch dialog when a submodule has the source branch, even if another lacks it; every repository is listed with whether it moves or keeps its branch, and why; with no submodule there is no toggle.
	it('starts the New branch toggle on by the offer, and lists which repositories move and which stay', () => {
		const on = createOf(newBranchOffer(sourceOf(local)!, roots, both, ws));
		expect(tag(on, 'data-sync-toggle', 'input')).toContain('checked');
		expect(tag(on, 'data-sync-root="."', 'li')).toContain('data-moves="true"');
		expect(tag(on, 'data-sync-root="lib"', 'li')).toContain('data-has="true"');
		expect(tag(on, 'data-sync-root="lib"', 'li')).toContain('data-moves="true"');
		expect(on).not.toContain('data-sync-problem');
		expect(on).toContain('>my-app<');

		const partial = createOf(newBranchOffer(sourceOf(local)!, roots, nestedLacks, ws));
		expect(tag(partial, 'data-sync-toggle', 'input')).toContain('checked');
		expect(tag(partial, 'data-sync-root="lib/nested"', 'li')).toContain('data-has="false"');
		expect(tag(partial, 'data-sync-root="lib/nested"', 'li')).toContain('data-moves="false"');
		expect(partial).toContain('stays on its branch: it has no feature/x');
		expect(tag(partial, 'data-submit', 'button')).not.toMatch(/\sdisabled/);

		const none = createOf(null);
		expect(none).not.toContain('data-sync-toggle');
		expect(none).not.toContain('data-sync-roots');
	});

	// Decision (ADR-0074 §3): with no submodule holding the branch the toggle starts off and the submodules are listed as keeping their branch; the project alone is made.
	it('starts the toggle off when no submodule has the branch', () => {
		const body = createOf(newBranchOffer(sourceOf(local)!, roots, noneHave, ws));
		expect(tag(body, 'data-sync-toggle', 'input')).not.toContain('checked');
		expect(tag(body, 'data-sync-toggle', 'input')).not.toMatch(/\sdisabled/);
		expect(tag(body, 'data-sync-root="lib"', 'li')).toContain('data-moves="false"');
		expect(tag(body, 'data-submit', 'button')).not.toMatch(/\sdisabled/);
	});

	// Decision (ADR-0074 §3): when the project lacks the branch the toggle is on and cannot be turned off, the dialog says why, and the project is listed as staying.
	it('forces the toggle on and explains it when the project lacks the branch', () => {
		const body = checkoutOf(checkoutOffer(local, roots, nestedLacks, wsLacks)!);
		expect(tag(body, 'data-sync-toggle', 'input')).toContain('checked');
		expect(tag(body, 'data-sync-toggle', 'input')).toMatch(/\sdisabled/);
		expect(body).toContain('data-sync-forced');
		expect(tag(body, 'data-sync-root="."', 'li')).toContain('data-moves="false"');
		expect(tag(body, 'data-sync-root="lib"', 'li')).toContain('data-moves="true"');
		expect(tag(body, 'data-sync-root="lib/nested"', 'li')).toContain('data-moves="false"');
		expect(tag(body, 'data-submit', 'button')).not.toMatch(/\sdisabled/);
		expect(checkoutOf(checkoutOffer(local, roots, both, ws)!)).not.toContain('data-sync-forced');
	});

	// Decision: nothing is called when no repository has the branch (the project lacks it and no submodule read does): Submit is off and the dialog says so.
	it('cannot be submitted when no repository would move', () => {
		const body = checkoutOf(checkoutOffer(local, roots, new Map([['lib', refsWith({ main: OID })]]), wsLacks)!);
		expect(body).toContain('data-sync-problem');
		expect(tag(body, 'data-submit', 'button')).toMatch(/\sdisabled/);
	});

	// Decision (ADR-0074 §3): a root whose refs are not read yet is listed as keeping its branch, with that reason, not as lacking the branch.
	it('lists a root whose refs are not read as pending', () => {
		const body = checkoutOf(checkoutOffer(local, roots, new Map([['lib', refsWith({ 'feature/x': LIB })]]), ws)!);
		expect(tag(body, 'data-sync-root="lib/nested"', 'li')).toContain('data-moves="false"');
		expect(body).toContain('its refs are not read yet');
		expect(body).not.toContain('it has no feature/x');
	});

	// Decision (ADR-0074 §3): the toolbar’s Create Branch lists a root that lacks the current branch as starting at its own HEAD, not as staying.
	it('says a root without the current branch starts at its own HEAD', () => {
		const head = { oid: A, branch: 'feature/x', detached: false, upstreamGone: false, ahead: 0, behind: 0 };
		const offer = newBranchOffer(headSource(head)!, roots, nestedLacks, ws)!;
		const body = createOf(offer);
		expect(tag(body, 'data-sync-root="lib/nested"', 'li')).toContain('data-moves="true"');
		expect(body).toContain('moves, starting at its own HEAD');
	});

	// Decision (ADR-0074 §3): the Checkout confirmation has the same toggle, with the same default rule.
	it('starts the Checkout toggle on or off by the offer', () => {
		expect(tag(checkoutOf(checkoutOffer(local, roots, both, ws)!), 'data-sync-toggle', 'input')).toContain('checked');
		const off = checkoutOf(checkoutOffer(local, roots, noneHave, ws)!);
		expect(tag(off, 'data-sync-toggle', 'input')).not.toContain('checked');
		expect(off).toContain('data-checkout-branch');
		expect(off).toContain('feature/x');
	});

	// Decision (ADR-0071 §6): after a stopped call each root is listed with its status as data and text, the failed one with git’s words as text and the skipped one without; none is markup.
	it('lists the outcomes per root as text', async () => {
		const m = model();
		await m.checkout(local, ['.', ...roots]);
		for (const body of [checkoutOf(checkoutOffer(local, roots, both, ws)!, m), createOf(newBranchOffer(sourceOf(local)!, roots, both, ws), m)]) {
			expect(tag(body, 'data-outcome-root="."', 'li')).toContain('data-status="done"');
			expect(tag(body, 'data-outcome-root="lib"', 'li')).toContain('data-status="failed"');
			expect(tag(body, 'data-outcome-root="lib/nested"', 'li')).toContain('data-status="skipped"');
			expect(body).toContain('git refused the checkout');
			expect(body).toContain('lib.txt');
			expect(body).toContain('commit or put the changes aside');
			expect(body).not.toContain('<img src=x');
			expect(body).toContain('&lt;img src=x');
			expect(body).not.toContain('data-op-error');
		}
	});

	// Decision: before any call there are no outcomes to list.
	it('lists no outcomes before a call', () => {
		expect(checkoutOf(checkoutOffer(local, roots, both, ws)!)).not.toContain('data-outcome-root');
	});
});
