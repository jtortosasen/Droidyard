import { describe, expect, it } from 'vitest';
import { capError, settle } from '../build/testing';
import {
	BINARY_TEXT,
	CommitText,
	DiffSource,
	TOO_LARGE_TEXT,
	commitFileOf,
	commitHeader,
	type CommitFile,
	type CommitTextClient,
} from './diffsource';

const C = 'c'.repeat(40);
const P1 = '1'.repeat(40);
const P2 = '2'.repeat(40);

const file = (over: Partial<CommitFile> = {}): CommitFile => ({ oid: C, parent: P1, path: 'src/A.kt', status: 'modified', binary: false, ...over });

const read = (text: string | undefined, over: Record<string, unknown> = {}) => ({
	path: 'x',
	base: 'x',
	exists: text !== undefined,
	size: 1,
	binary: false,
	tooLarge: false,
	...(text !== undefined ? { text } : {}),
	...over,
});

interface Call {
	path: string;
	base: string | undefined;
	workspace?: string;
}

/** A `git.read` that answers by the base it was asked at: the commit's text and the parent's. */
function setup(answer: (c: Call) => unknown) {
	const calls: Call[] = [];
	const client = {
		call: async (_n: string, p: { path: string; base?: string }, o?: { workspace?: string }) => {
			const c = { path: p.path, base: p.base, workspace: o?.workspace };
			calls.push(c);
			const r = answer(c);
			if (r instanceof Error) throw r;
			return r;
		},
	} as unknown as CommitTextClient;
	return { calls, text: new CommitText(client, { root: ' /proj ' }) };
}
const byBase = (c: Call) => read(c.base === C ? 'NEW' : 'OLD');

describe('CommitText', () => {
	// Decision (ADR-0070 §4, ADR-0069 §5): a commit's file is its text at the commit against its text at the first parent,
	// each read with `base` set to that commit's full oid, in the project.
	it('reads the file at the commit and at its first parent', async () => {
		const t = setup(byBase);
		t.text.follow(file());
		expect(t.text.view).toEqual({ kind: 'loading' });
		await settle();
		expect(t.text.view).toEqual({ kind: 'text', before: 'OLD', after: 'NEW' });
		expect(t.calls).toEqual(
			expect.arrayContaining([
				{ path: 'src/A.kt', base: P1, workspace: '/proj' },
				{ path: 'src/A.kt', base: C, workspace: '/proj' },
			]),
		);
		expect(t.calls).toHaveLength(2);
	});

	// Decision: a rename is read at the path the parent holds it under, and at the new path at the commit.
	it('reads a rename at its origPath at the parent', async () => {
		const t = setup(byBase);
		t.text.follow(file({ path: 'new/B.kt', origPath: 'old/B.kt', status: 'renamed' }));
		await settle();
		expect(t.calls).toEqual(
			expect.arrayContaining([
				{ path: 'old/B.kt', base: P1, workspace: '/proj' },
				{ path: 'new/B.kt', base: C, workspace: '/proj' },
			]),
		);
		expect(t.calls).toHaveLength(2);
	});

	// Decision: a merge's file is read against the FIRST parent (the one git.show diffed against); the second is never read.
	it('reads a merge against its first parent', async () => {
		const f = commitFileOf({ oid: C, parents: [P1, P2] }, { path: 'src/A.kt', status: 'modified', binary: false });
		expect(f.parent).toBe(P1);
		const t = setup(byBase);
		t.text.follow(f);
		await settle();
		expect(t.calls.map((c) => c.base).sort()).toEqual([C, P1].sort());
		expect(t.calls.some((c) => c.base === P2)).toBe(false);
	});

	// Decision: an added file has an empty base (nothing is read at the parent); a root commit has no parent to read at.
	it('gives an added file, or a root commit’s file, an empty base without reading the parent', async () => {
		const added = setup(byBase);
		added.text.follow(file({ status: 'added' }));
		await settle();
		expect(added.text.view).toEqual({ kind: 'text', before: '', after: 'NEW' });
		expect(added.calls).toEqual([{ path: 'src/A.kt', base: C, workspace: '/proj' }]);
		const root = setup(byBase);
		root.text.follow(commitFileOf({ oid: C, parents: [] }, { path: 'src/A.kt', status: 'added', binary: false }));
		await settle();
		expect(root.text.view).toEqual({ kind: 'text', before: '', after: 'NEW' });
		expect(root.calls.every((c) => c.base === C)).toBe(true);
	});

	// Decision: a deleted file has no text at the commit (nothing is read there): the right pane alone shows the parent's.
	it('gives a deleted file no text at the commit and does not read it there', async () => {
		const t = setup(byBase);
		t.text.follow(file({ status: 'deleted' }));
		await settle();
		expect(t.text.view).toEqual({ kind: 'text', before: 'OLD', after: null });
		expect(t.calls).toEqual([{ path: 'src/A.kt', base: P1, workspace: '/proj' }]);
	});

	// Decision: a binary file is a message and nothing is read (git.show already said so); one the read finds binary or too large, on either side, is a message too.
	it('turns binary, too large and an error into messages', async () => {
		const bin = setup(byBase);
		bin.text.follow(file({ binary: true }));
		expect(bin.text.view).toEqual({ kind: 'message', text: BINARY_TEXT });
		expect(bin.calls).toHaveLength(0);
		const rbin = setup((c) => (c.base === C ? read(undefined, { exists: true, binary: true }) : read('OLD')));
		rbin.text.follow(file());
		await settle();
		expect(rbin.text.view).toEqual({ kind: 'message', text: BINARY_TEXT });
		const big = setup((c) => (c.base === P1 ? read(undefined, { exists: true, tooLarge: true }) : read('NEW')));
		big.text.follow(file());
		await settle();
		expect(big.text.view).toEqual({ kind: 'message', text: TOO_LARGE_TEXT });
		const err = setup(() => capError('GIT_BAD_REVISION', 'no such commit'));
		err.text.follow(file());
		await settle();
		expect(err.text.view.kind).toBe('message');
		expect((err.text.view as { text: string }).text).toContain('GIT_BAD_REVISION');
	});

	// Decision: the same file again reads nothing; another file (or commit) reads again; null stops.
	it('reads again for another file and not for the same one', async () => {
		const t = setup(byBase);
		t.text.follow(file());
		await settle();
		t.text.follow(file());
		expect(t.calls).toHaveLength(2);
		t.text.follow(file({ path: 'src/B.kt' }));
		await settle();
		expect(t.calls).toHaveLength(4);
		t.text.follow(null);
		expect(t.text.view).toEqual({ kind: 'idle' });
		t.text.follow(file({ path: 'src/B.kt' })); // after null the key is forgotten: read again
		await settle();
		expect(t.calls).toHaveLength(6);
	});

	// Decision: an answer for a file the tab has moved off is ignored.
	it('ignores an answer that a newer file overtook', async () => {
		const pending: { path: string; res: (r: unknown) => void }[] = [];
		const client = { call: (_n: string, p: { path: string }) => new Promise((res) => pending.push({ path: p.path, res })) } as unknown as CommitTextClient;
		const text = new CommitText(client, { root: '/p' });
		text.follow(file({ path: 'a.kt', status: 'added' }));
		text.follow(file({ path: 'b.kt', status: 'added' }));
		expect(pending.map((p) => p.path)).toEqual(['a.kt', 'b.kt']);
		pending[1]!.res(read('B'));
		await settle();
		pending[0]!.res(read('A'));
		await settle();
		expect(text.view).toEqual({ kind: 'text', before: '', after: 'B' });
	});
});

describe('DiffSource', () => {
	// Decision (ADR-0070 §4): the tab starts on the working tree, shows a commit's file when told, and returns when told.
	it('switches to a commit’s file and back to the working tree', () => {
		const s = new DiffSource();
		const seen: string[] = [];
		s.subscribe(() => seen.push(s.view.kind));
		expect(s.view).toEqual({ kind: 'worktree' });
		s.showCommit(file());
		expect(s.view).toEqual({ kind: 'commit', file: file() });
		s.showWorktree();
		expect(s.view).toEqual({ kind: 'worktree' });
		expect(seen).toEqual(['commit', 'worktree']);
	});

	// Decision: the same file again, or the working tree while on it, changes nothing (the view on it is not made again).
	it('says nothing when the source does not change', () => {
		const s = new DiffSource();
		let n = 0;
		s.subscribe(() => n++);
		s.showWorktree();
		expect(n).toBe(0);
		s.showCommit(file());
		s.showCommit(file());
		expect(n).toBe(1);
		s.showCommit(file({ path: 'src/B.kt' }));
		expect(n).toBe(2);
		s.showCommit(file({ oid: P2 }));
		expect(n).toBe(3);
	});
});

describe('the header', () => {
	// Decision (owner, item 9): `<path> · <short> vs <parent short>`, the hashes at seven characters; a root commit has no parent to name.
	it('names the path, the commit and its parent', () => {
		expect(commitHeader(file())).toBe('src/A.kt · ccccccc vs 1111111');
		expect(commitHeader(file({ parent: null }))).toBe('src/A.kt · ccccccc vs no parent');
	});
});

describe('commitFileOf', () => {
	// Decision: the parent is the first of `parents` (git.show's own base), and an origPath is kept only for a rename.
	it('takes the first parent and the rename’s origPath', () => {
		const f = commitFileOf({ oid: C, parents: [P1, P2] }, { path: 'n.kt', origPath: 'o.kt', status: 'renamed', binary: false });
		expect(f).toEqual({ oid: C, parent: P1, path: 'n.kt', origPath: 'o.kt', status: 'renamed', binary: false });
		expect('origPath' in commitFileOf({ oid: C, parents: [P1] }, { path: 'n.kt', status: 'modified', binary: false })).toBe(false);
	});
});
