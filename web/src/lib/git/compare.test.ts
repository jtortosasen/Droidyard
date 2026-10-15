import { render } from 'svelte/server';
import { describe, expect, it } from 'vitest';
import { capError, settle } from '../build/testing';
import { CompareModel, aheadTitle, behindTitle, type CompareClient, type CompareTarget } from './compare';
import CompareView from './CompareView.svelte';
import { compareFileOf } from './diffsource';
import { chain, showOf } from './historytesting';

// Compare with current (plan m2n P9; ADR-0072): the three reads, the view's markup and the file it opens.

const TIP = 'a'.repeat(40);
const HEAD = 'b'.repeat(40);
const target: CompareTarget = { branch: 'feature/x', current: 'main', tip: TIP, head: HEAD };
const page = (commits: unknown[], more = false) => ({ commits, tips: [], tipsTruncated: false, more });

interface Call {
	name: string;
	params: Record<string, unknown>;
	workspace?: string;
}

function setup(over: { log?: (p: Record<string, unknown>) => unknown; show?: () => unknown } = {}) {
	const calls: Call[] = [];
	const client = {
		call: async (name: string, params: Record<string, unknown>, opts?: { workspace?: string }) => {
			calls.push({ name, params, workspace: opts?.workspace });
			const r = name === 'git.log' ? (over.log ?? (() => page([])))(params) : (over.show ?? (() => showOf(TIP)))();
			if (r instanceof Error) throw r;
			return r;
		},
	} as unknown as CompareClient;
	return { calls, model: new CompareModel(client, { root: ' /p ' }) };
}

describe('CompareModel', () => {
	// Decision (ADR-0072 Consequences): two log pages, each from one tip excluding the other, and one show of the branch's tip with HEAD as base, all in the project.
	it('reads the commits each way and the files between the tips', async () => {
		const t = setup();
		t.model.open(target);
		await settle();
		const logs = t.calls.filter((c) => c.name === 'git.log').map((c) => c.params);
		expect(logs).toContainEqual({ from: [TIP], exclude: [HEAD], skip: 0, limit: 200 });
		expect(logs).toContainEqual({ from: [HEAD], exclude: [TIP], skip: 0, limit: 200 });
		expect(t.calls.filter((c) => c.name === 'git.show').map((c) => c.params)).toEqual([{ oid: TIP, base: HEAD }]);
		expect(t.calls.every((c) => c.workspace === '/p')).toBe(true);
		expect(t.model.view.ahead.state).toBe('ready');
		expect(t.model.view.files.state).toBe('ready');
	});

	// Decision (ADR-0069 §2, ADR-0072 §1): a next page passes the same tips with skip = the commits received.
	it('pages a list by skip', async () => {
		const t = setup({ log: (p) => (p.skip === 0 ? page(chain(3), true) : page(chain(2, 'd'))) });
		t.model.open(target);
		await settle();
		t.model.loadMore('ahead');
		await settle();
		const second = t.calls.map((c) => c.params).filter((p) => p.skip === 3);
		expect(second).toEqual([{ from: [TIP], exclude: [HEAD], skip: 3, limit: 200 }]);
		expect(t.model.view.ahead.commits).toHaveLength(5);
	});

	// Decision: an answer that a close overtook is dropped.
	it('drops the answers of a view that was closed', async () => {
		const t = setup();
		t.model.open(target);
		t.model.close();
		await settle();
		expect(t.model.view.open).toBe(false);
		expect(t.model.view.ahead.state).toBe('loading');
	});

	it('says why a read failed and reads the list again on retry', async () => {
		let fail = true;
		const t = setup({ log: () => (fail ? capError('GIT_FAILED', 'git failed') : page(chain(1))) });
		t.model.open(target);
		await settle();
		expect(t.model.view.ahead.state).toBe('error');
		expect(t.model.view.ahead.problem).toContain('git failed');
		fail = false;
		t.model.retry('ahead');
		await settle();
		expect(t.model.view.ahead.state).toBe('ready');
	});
});

describe('compareFileOf', () => {
	// Decision (ADR-0072 §3): the file is read at the tip (after) and at the base, HEAD (before); a rename is read at its old path at the base.
	it('reads the tip against the base', () => {
		expect(compareFileOf(TIP, HEAD, { path: 'n.txt', origPath: 'o.txt', status: 'renamed', binary: false })).toEqual({
			oid: TIP,
			parent: HEAD,
			path: 'n.txt',
			origPath: 'o.txt',
			status: 'renamed',
			binary: false,
			root: '.', // Compare reads the workspace's own repository (P7 added the root to a commit file)
		});
	});
});

describe('CompareView markup', () => {
	async function view(branch = 'feature/x') {
		const t = setup({
			log: (p) => page(Array.isArray(p.from) && p.from[0] === TIP ? [{ ...chain(1)[0], oid: 'ahead1', subject: '<b>hi' }] : []),
			show: () =>
				showOf(TIP, {
					files: [
						{ path: 'src/a.txt', status: 'modified', added: 2, removed: 1, binary: false },
						{ path: 'img.png', status: 'added', binary: true },
					],
				}),
		});
		t.model.open({ ...target, branch });
		await settle();
		return render(CompareView, { props: { model: t.model, view: t.model.view, onopenfile: () => {}, now: Date.now() } }).body;
	}

	// Decision (plan P9): the hooks the level-4 checks read; the titles say which way; texts from git are escaped.
	it('has the lists, the files with their counts, and a close', async () => {
		const body = await view('<i>x');
		expect(body).toContain('data-compare-view');
		expect(body).toContain('data-compare-list="ahead"');
		expect(body).toContain('data-compare-list="behind"');
		expect(body).toContain('data-oid="ahead1"');
		expect(body).toContain(aheadTitle('&lt;i>x', 'main'));
		expect(body).toContain(behindTitle('&lt;i>x', 'main'));
		expect(body).toMatch(/data-compare-file="src\/a.txt"[^>]*data-added="2"[^>]*data-removed="1"[^>]*data-binary="false"/);
		expect(body).toMatch(/data-compare-file="img.png"[^>]*data-binary="true"/);
		expect(body).toContain('data-compare-close');
		expect(body).not.toContain('<b>hi');
		expect(body).toContain('&lt;b>hi');
		expect(body).not.toContain('<i>x');
	});
});
