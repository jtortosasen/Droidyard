import { render } from 'svelte/server';
import { describe, expect, it } from 'vitest';
import type { Result } from '../api/capabilities.gen';
import { CapabilityError, type CallOptions } from '../api/client';
import { BranchModel, type BranchClient } from './branch';
import { checkoutOffer, checkoutParams, createParams, headSource, newBranchOffer, sourceOf, syncOn, type BranchTarget } from './branchops';
import CheckoutDialog from './CheckoutDialog.svelte';
import CreateBranchDialog from './CreateBranchDialog.svelte';
import { refsOf } from './historytesting';

// Synchronised branches (plan m2n P8; ADR-0071 §6): which submodules a checkout or a new branch can be
// applied to, the toggle's default, each root's start, the parameters of the call, and the outcomes listed per root.

const A = 'a'.repeat(40);
const LIB = 'b'.repeat(40);
const NESTED = 'c'.repeat(40);
const OID = 'd'.repeat(40);

type Refs = Result<'git.refs'>;
const roots = ['lib', 'lib/nested'];
const tag = (body: string, attr: string, el: string) => new RegExp(`<${el}[^>]*${attr}[^>]*>`).exec(body)?.[0] ?? '';

/** A submodule's refs with the given local branches (name -> oid) and remote-tracking ones ("origin/name" -> oid). */
function refsWith(locals: Record<string, string>, remotes: Record<string, string> = {}): Refs {
	return refsOf([OID], {
		branches: Object.entries(locals).map(([name, oid]) => ({ name, oid })),
		remotes: Object.entries(remotes).map(([ref, oid]) => ({ remote: ref.split('/')[0], name: ref.split('/').slice(1).join('/'), oid })),
	});
}

const both = new Map<string, Refs>([
	['lib', refsWith({ main: OID, 'feature/x': LIB })],
	['lib/nested', refsWith({ main: OID, 'feature/x': NESTED })],
]);
const nestedLacks = new Map<string, Refs>([
	['lib', refsWith({ main: OID, 'feature/x': LIB })],
	['lib/nested', refsWith({ main: OID })],
]);
const local: BranchTarget = { kind: 'local', name: 'feature/x', oid: A };

describe('which submodules have the branch, and the toggle’s default', () => {
	// Decision (ADR-0071 §6): the toggle starts on when every initialised submodule has the branch checked out, and off when one lacks it; a project with no submodule offers nothing.
	it('is on for a checkout when every submodule has the branch, off when one lacks it', () => {
		const on = checkoutOffer(local, roots, both);
		expect(on?.defaultOn).toBe(true);
		expect(on?.missing).toEqual([]);
		const off = checkoutOffer(local, roots, nestedLacks);
		expect(off?.defaultOn).toBe(false);
		expect(off?.missing).toEqual(['lib/nested']);
		expect(checkoutOffer(local, [], both)).toBeNull();
	});

	// Decision (ADR-0071 §6): a root whose refs are not read yet is a root that lacks the branch for now, so the default is off, not on.
	it('is off while a submodule’s refs are not read', () => {
		const offer = checkoutOffer(local, roots, new Map([['lib', refsWith({ 'feature/x': LIB })]]));
		expect(offer?.defaultOn).toBe(false);
		expect(offer?.missing).toEqual(['lib/nested']);
	});

	// Decision (the daemon's rule: the remote must be one of every root's remotes): a remote-tracking target is checked out where the submodule has the remote and either that remote's branch or the local branch of that name; a local branch of that name in a root with no such remote does not count, nor does another remote's branch.
	it('needs the remote in each submodule for a remote-tracking checkout', () => {
		const remote: BranchTarget = { kind: 'remote', remote: 'origin', name: 'feature/y', oid: A };
		const tracking = new Map([
			['lib', refsWith({}, { 'origin/feature/y': LIB })],
			['lib/nested', refsWith({ 'feature/y': NESTED }, { 'origin/main': OID })],
		]);
		expect(checkoutOffer(remote, roots, tracking)?.defaultOn).toBe(true);
		const other = new Map([...tracking, ['lib', refsWith({}, { 'upstream/feature/y': LIB })]]);
		expect(checkoutOffer(remote, roots, other)?.missing).toEqual(['lib']);
		// the local branch is there, the remote is not
		const noRemote = new Map([...tracking, ['lib/nested', refsWith({ 'feature/y': NESTED })]]);
		const offer = checkoutOffer(remote, roots, noRemote);
		expect(offer?.missing).toEqual(['lib/nested']);
		expect(offer?.defaultOn).toBe(false);
		// a local target needs no remote
		expect(checkoutOffer({ kind: 'local', name: 'feature/y' }, roots, noRemote)?.missing).toEqual(['lib']);
	});

	// Decision: the toggle follows the offer until the person touches it, so refs that arrive after the dialog opened turn it on; once set, it stays as set.
	it('follows the offer’s default until the toggle is touched', () => {
		const source = sourceOf(local)!;
		const early = newBranchOffer(source, roots, new Map([['lib', refsWith({ 'feature/x': LIB })]]));
		const late = newBranchOffer(source, roots, both);
		expect(syncOn(null, early)).toBe(false);
		expect(syncOn(null, late)).toBe(true);
		expect(syncOn(false, late)).toBe(false);
		expect(syncOn(true, early)).toBe(true);
		expect(syncOn(true, null)).toBe(false);
		expect(syncOn(null, null)).toBe(false);
	});

	// Decision (ADR-0071 §6, plan Q6 (1)): a new branch from a branch starts, in each submodule, at that branch's oid there; the same default rule.
	it('takes each submodule’s start from the source branch there', () => {
		const source = sourceOf(local)!;
		const offer = newBranchOffer(source, roots, both);
		expect(offer?.defaultOn).toBe(true);
		expect([...(offer?.starts ?? [])]).toEqual([
			['lib', LIB],
			['lib/nested', NESTED],
		]);
		const partial = newBranchOffer(source, roots, nestedLacks);
		expect(partial?.defaultOn).toBe(false);
		expect(partial?.missing).toEqual(['lib/nested']);
		// A remote-tracking source is looked for among the remote-tracking branches, not the local ones.
		const remoteSource = sourceOf({ kind: 'remote', remote: 'origin', name: 'feature/x', oid: A })!;
		expect(newBranchOffer(remoteSource, roots, both)?.missing).toEqual(roots);
	});

	// Decision (ADR-0071 §6): the toolbar's Create Branch starts at HEAD, so the branch looked for is the current one; a detached HEAD has none, and the toggle starts off.
	it('looks for HEAD’s branch for the toolbar’s Create Branch, and for nothing when HEAD is detached', () => {
		const head = { oid: A, branch: 'feature/x', detached: false, upstreamGone: false, ahead: 0, behind: 0 };
		expect(newBranchOffer(headSource(head)!, roots, both)?.defaultOn).toBe(true);
		const detached = newBranchOffer(headSource({ ...head, branch: undefined, detached: true })!, roots, both);
		expect(detached?.defaultOn).toBe(false);
		expect(detached?.missing).toEqual(roots);
		expect(detached?.branch).toBe('');
	});
});

describe('the parameters of a synchronised call', () => {
	// Decision (ADR-0071 §6): roots are the project first, then the submodules; without any the call is the one of P3, with no roots.
	it('adds roots to a checkout only when submodules are synchronised', () => {
		expect(checkoutParams(local)).toEqual({ name: 'feature/x' });
		expect(checkoutParams(local, [])).toEqual({ name: 'feature/x' });
		expect(checkoutParams(local, roots)).toEqual({ name: 'feature/x', roots: ['.', 'lib', 'lib/nested'] });
		expect(checkoutParams({ kind: 'remote', remote: 'origin', name: 'y' }, ['lib'])).toEqual({ name: 'y', remote: 'origin', roots: ['.', 'lib'] });
	});

	// Decision (plan Q6 (1)): each submodule's start is its own oid in fromIn, and `from` stays the project's; the root without one has no entry (the daemon refuses it).
	it('gives a new branch one start per submodule in fromIn', () => {
		const offer = newBranchOffer(sourceOf(local)!, roots, both)!;
		expect(createParams('n', true, A, { roots: offer.roots, starts: offer.starts })).toEqual({
			name: 'n',
			checkout: true,
			from: A,
			roots: ['.', 'lib', 'lib/nested'],
			fromIn: { lib: LIB, 'lib/nested': NESTED },
		});
		const partial = newBranchOffer(sourceOf(local)!, roots, nestedLacks)!;
		expect(createParams('n', false, A, { roots: partial.roots, starts: partial.starts }).fromIn).toEqual({ lib: LIB });
		expect(createParams('n', false, A)).toEqual({ name: 'n', checkout: false, from: A });
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
		expect(await f.model.checkout(local, roots)).toBe(false);
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
		expect(await all.model.createBranch('n', true, A, { roots: ['lib'], starts: new Map([['lib', LIB]]) })).toBe(true);
		expect(all.calls[0].params).toEqual({ name: 'n', checkout: true, from: A, roots: ['.', 'lib'], fromIn: { lib: LIB } });
		expect(all.model.view.outcomes.map((o) => o.status)).toEqual(['done', 'done']);

		const one = fakeClient({ outcomes: [{ root: '.', status: 'failed', error: { code: 'GIT_FAILED', message: 'no', cause: 'error: x' } }] });
		expect(await one.model.checkout(local)).toBe(false);
		expect(one.model.view.outcomes).toEqual([]);
		expect(one.model.view.failure?.cause).toBe('error: x');
	});

	// Decision (ADR-0071 §6): a call refused whole (a root lacking the branch) applied nothing: the daemon's refusal is the failure and no root is listed.
	it('shows a refused call as a failure and no outcomes', async () => {
		const f = fakeClient(new CapabilityError({ code: 'REQUEST_INVALID_PARAMS', message: 'there is no local branch "x" (in the submodule lib)' }, false));
		expect(await f.model.checkout(local, roots)).toBe(false);
		expect(f.model.view.outcomes).toEqual([]);
		expect(f.model.view.failure?.message).toMatch(/submodule lib/);
	});
});

describe('the dialogs’ markup', () => {
	const model = (answer: unknown = stopped) => fakeClient(answer).model;
	const createOf = (offer: ReturnType<typeof newBranchOffer>, m = model()) =>
		render(CreateBranchDialog, { props: { source: { ...sourceOf(local)!, proposed: 'newb' }, locals: new Set(['main']), model: m, view: m.view, offer, onsubmit: async () => true, oncancel: () => {} } }).body;
	const checkoutOf = (offer: NonNullable<ReturnType<typeof checkoutOffer>>, m = model()) =>
		render(CheckoutDialog, { props: { target: local, offer, model: m, view: m.view, onsubmit: async () => true, oncancel: () => {} } }).body;

	// Decision (ADR-0071 §6): the toggle is on in the New branch dialog when every submodule has the source branch and off when one lacks it; the submodules are listed with whether each has it; with none there is no toggle.
	it('starts the New branch toggle on or off by the offer, and lists the submodules', () => {
		const on = createOf(newBranchOffer(sourceOf(local)!, roots, both));
		expect(tag(on, 'data-sync-toggle', 'input')).toContain('checked');
		expect(on).toContain('data-sync-roots');
		expect(tag(on, 'data-sync-root="lib"', 'li')).toContain('data-has="true"');
		expect(on).toContain('has feature/x');
		expect(on).not.toContain('data-sync-problem');

		const off = createOf(newBranchOffer(sourceOf(local)!, roots, nestedLacks));
		expect(tag(off, 'data-sync-toggle', 'input')).not.toContain('checked');
		expect(tag(off, 'data-sync-root="lib/nested"', 'li')).toContain('data-has="false"');
		expect(off).toContain('has no feature/x');
		// off: nothing stops Create, since the project alone is made
		expect(tag(off, 'data-submit', 'button')).not.toMatch(/\sdisabled/);

		const none = createOf(null);
		expect(none).not.toContain('data-sync-toggle');
		expect(none).not.toContain('data-sync-roots');
	});

	// Decision (ADR-0071 §6): the Checkout confirmation has the same toggle, with the same default rule.
	it('starts the Checkout toggle on or off by the offer', () => {
		expect(tag(checkoutOf(checkoutOffer(local, roots, both)!), 'data-sync-toggle', 'input')).toContain('checked');
		const off = checkoutOf(checkoutOffer(local, roots, nestedLacks)!);
		expect(tag(off, 'data-sync-toggle', 'input')).not.toContain('checked');
		expect(off).toContain('data-checkout-branch');
		expect(off).toContain('feature/x');
	});

	// Decision (ADR-0071 §6): after a stopped call each root is listed with its status as data and text, the failed one with git's words as text and the skipped one without; none is markup.
	it('lists the outcomes per root as text', async () => {
		const m = model();
		await m.checkout(local, roots);
		for (const body of [checkoutOf(checkoutOffer(local, roots, both)!, m), createOf(newBranchOffer(sourceOf(local)!, roots, both), m)]) {
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
		expect(checkoutOf(checkoutOffer(local, roots, both)!)).not.toContain('data-outcome-root');
	});
});
