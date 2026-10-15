import { render } from 'svelte/server';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { settle } from '../build/testing';
import { BranchModel, type BranchClient } from './branch';
import { sourceOf } from './branchops';
import CreateBranchDialog from './CreateBranchDialog.svelte';
import { FetchModel, type FetchClient } from './fetch';
import HistoryPanel from './HistoryPanel.svelte';
import { chain, refsOf, setup, submodulesOf, idleUpdater } from './historytesting';
import type { GitWatch } from './watch';

// The markup of Checkout and Create Branch (plan m2n P3): the dialog, and the History tab with
// its branch menu on local and remote-tracking branches, the dirty-buffer block and the failed
// outcome. Rendered on the server, as the other markup tests: effects do not run.
afterEach(() => vi.unstubAllGlobals());

const OID = 'b'.repeat(40);

function model(answer: unknown, dirty: string[] = []) {
	const client = { call: () => Promise.resolve(answer) } as unknown as BranchClient;
	return new BranchModel(client, { root: '/p' }, { dirtyPaths: () => dirty }, () => {});
}

const done = { outcomes: [{ root: '.', status: 'done' }] };
const refused = {
	outcomes: [{ root: '.', status: 'failed', error: { code: 'GIT_FAILED', message: 'git refused the checkout', cause: 'error: <img src=x onerror=1>\n\tshared.txt', nextStep: 'commit or put the changes aside' } }],
};
const tag = (body: string, attr: string, el = 'button') => new RegExp(`<${el}[^>]*${attr}[^>]*>`).exec(body)?.[0] ?? '';

describe('the Create Branch dialog', () => {
	const open = (m: BranchModel, over: Partial<Parameters<typeof CreateBranchDialog>[1]> = {}) =>
		render(CreateBranchDialog, {
			props: {
				source: sourceOf({ kind: 'remote', remote: 'origin', name: 'feature/y', oid: OID })!,
				locals: new Set(['main']),
				model: m,
				view: m.view,
				onsubmit: async () => true,
				oncancel: () => {},
				...over,
			},
		}).body;

	// Decision (plan P3): the dialog shows where the branch starts, with the short oid, a name proposed from a remote-tracking branch without its remote, and Check out on by default.
	it('shows the start, the proposed name and Check out on', () => {
		const body = open(model(done));
		expect(body).toContain('data-create-branch-dialog');
		expect(body).toContain('origin/feature/y');
		expect(body).toContain('bbbbbbb');
		expect(body).not.toContain(OID);
		expect(tag(body, 'data-name-input', 'input')).toContain('value="feature/y"');
		expect(tag(body, 'data-checkout-toggle', 'input')).toContain('checked');
		expect(tag(body, 'data-submit')).not.toMatch(/\sdisabled/);
		expect(body).not.toContain('data-dirty-block');
		expect(body).not.toContain('data-op-error');
	});

	// Decision (ADR-0071 §5): with Check out on and an unsaved buffer the dialog names the files, and Create waits for the user to save or turn Check out off.
	it('names the unsaved files that stop a checkout', () => {
		const body = open(model(done, ['src/a.kt', '<b>x.kt']));
		expect(body).toContain('data-dirty-block');
		expect(body).toContain('src/a.kt');
		expect(body).toContain('&lt;b>x.kt');
		expect(body).not.toContain('<b>x.kt');
	});

	// Decision: a name the list holds is refused before the call, and an empty one cannot be submitted.
	it('refuses a name that exists and an empty one', () => {
		const exists = open(model(done), { source: { label: 'main', oid: OID, proposed: 'main' } });
		expect(exists).toContain('data-name-problem');
		expect(exists).toContain('already exists');
		expect(tag(exists, 'data-submit')).toMatch(/\sdisabled/);
		const empty = open(model(done), { source: { label: 'main', oid: OID, proposed: '' } });
		expect(tag(empty, 'data-submit')).toMatch(/\sdisabled/);
	});

	// Decision (ADR-0071 §5): a failed call stays in the dialog with git's own words as text, never markup.
	it('keeps a failed call in the dialog with its cause as text', async () => {
		const m = model(refused);
		await m.createBranch('n', false, OID);
		const body = open(m);
		expect(body).toContain('data-op-error');
		expect(body).toContain('git refused the checkout');
		expect(body).toContain('shared.txt');
		expect(body).toContain('commit or put the changes aside');
		expect(body).not.toContain('<img src=x');
		expect(body).toContain('&lt;img src=x');
	});
});

describe('the History tab’s branch controls', () => {
	async function panel(branches: BranchModel) {
		vi.stubGlobal('window', {});
		const t = setup(() => chain(3), 10);
		await settle();
		t.state.refs = refsOf(['c0'], {
			head: { oid: 'c1', branch: 'feature/x', detached: false, upstreamGone: false, ahead: 0, behind: 0 },
			branches: [
				{ name: 'main', oid: 'c0' },
				{ name: 'feature/x', oid: 'c1' },
			],
			remotes: [
				{ remote: 'origin', name: 'HEAD', oid: 'c0' },
				{ remote: 'origin', name: 'feature/y', oid: 'c2' },
			],
			tags: [{ name: 'v1', oid: 'c1' }],
		});
		t.store.refreshRefs();
		await settle();
		const fetcher = new FetchModel({ call: (() => Promise.resolve({})) as FetchClient['call'] }, { root: '/p' }, () => {});
		const submodules = await submodulesOf();
		return render(HistoryPanel, { props: { store: t.store, watch: t.watch as unknown as GitWatch, workspaceId: 'ws1', fetcher, branches, submodules, updater: idleUpdater() } }).body;
	}

	// Decision (plan P3): the branch menu opens on a local and on a remote-tracking branch, not on a tag or on origin/HEAD.
	it('gives a menu button to every branch listed, and to nothing else', async () => {
		const body = await panel(model(done));
		// The folders start closed (plan R1) except the current branch's: `feature/` holds it, `origin/` is closed, so the
		// remote-tracking branch is not listed here (its menu is reached at level 4).
		expect([...body.matchAll(/data-branch-menu/g)]).toHaveLength(2);
		expect(body).toMatch(/aria-expanded="false"[^>]*data-folder="remotes:origin"/);
		expect(body).not.toContain('data-ref="refs/remotes/origin/feature/y"');
		for (const ref of ['refs/heads/main', 'refs/heads/feature/x']) {
			const row = new RegExp(`data-ref="${ref}"[\\s\\S]*?</div>`).exec(body)?.[0] ?? '';
			expect(row, ref).toContain('data-branch-menu');
		}
		expect(/data-ref="refs\/tags\/v1"[\s\S]*?<\/div>/.exec(body)?.[0] ?? '').not.toContain('data-branch-menu');
		expect(body).not.toContain('refs/remotes/origin/HEAD');
	});

	// Decision (ADR-0071 §5): a checkout stopped by a dirty buffer shows the block with the files in the tab, and no failure.
	it('shows the dirty-buffer block with its files', async () => {
		const m = model(done, ['a.kt', 'b/c.kt']);
		await m.checkout({ kind: 'local', name: 'feature/x' });
		const body = await panel(m);
		expect(body).toContain('data-dirty-block');
		expect(body).toContain('a.kt');
		expect(body).toContain('b/c.kt');
		expect(body).not.toContain('data-op-error');
	});

	// Decision (ADR-0071 §5): a failed outcome shows git's cause as text, and the toolbar's Create Branch is enabled once HEAD is known.
	it('shows a failed outcome’s cause as text and enables Create Branch', async () => {
		const m = model(refused);
		await m.checkout({ kind: 'local', name: 'feature/x' });
		const body = await panel(m);
		expect(body).toContain('data-op-error');
		expect(body).toContain('shared.txt');
		expect(body).toContain('commit or put the changes aside');
		expect(body).not.toContain('<img src=x');
		expect(body).not.toContain('data-dirty-block');
		expect(tag(body, 'data-toolbar="create-branch"')).not.toMatch(/\sdisabled/);
	});
});
