import { describe, expect, it } from 'vitest';
import { capError, settle } from '../build/testing';
import { BINARY_TEXT, HeadBase, TOO_LARGE_TEXT, type BaseClient } from './base';

function setup(answer: (path: string) => unknown) {
	const calls: { path: string; workspace?: string }[] = [];
	const client = {
		call: async (_n: string, p: { path: string }, o?: { workspace?: string }) => {
			calls.push({ path: p.path, workspace: o?.workspace });
			const r = answer(p.path);
			if (r instanceof Error) throw r;
			return r;
		},
	} as unknown as BaseClient;
	return { calls, base: new HeadBase(client, { root: ' /proj ' }) };
}
const read = (text: string | undefined, over: Record<string, unknown> = {}) => ({
	path: 'a.kt',
	base: 'HEAD',
	exists: text !== undefined,
	size: 1,
	binary: false,
	tooLarge: false,
	...(text !== undefined ? { text } : {}),
	...over,
});

describe('HeadBase', () => {
	// Decision (ADR-0067 §1): HEAD's text, as committed, is what the buffer is compared with; the workspace is the trimmed root.
	it('reads HEAD’s text of the file in the project', async () => {
		const t = setup(() => read('old\r\n'));
		t.base.follow({ path: 'a.kt', mark: 'modified', oid: 'c1' });
		expect(t.base.view).toEqual({ kind: 'loading' });
		await settle();
		expect(t.base.view).toEqual({ kind: 'text', text: 'old\r\n', exists: true });
		expect(t.calls).toEqual([{ path: 'a.kt', workspace: '/proj' }]);
	});

	// Decision (plan step 5): a renamed file is read at the path HEAD holds it under.
	it('reads a rename at its origPath', async () => {
		const t = setup(() => read('x'));
		t.base.follow({ path: 'new.kt', mark: 'renamed', origPath: 'old.kt' });
		await settle();
		expect(t.calls.map((c) => c.path)).toEqual(['old.kt']);
	});

	// Decision: read again when HEAD's oid or the file's entry changes, and not otherwise.
	it('reads again on a new oid or entry and not on the same input', async () => {
		const t = setup(() => read('x'));
		const input = { path: 'a.kt', mark: 'modified' as const, oid: 'c1' };
		t.base.follow(input);
		await settle();
		t.base.follow({ ...input });
		expect(t.calls).toHaveLength(1);
		t.base.follow({ ...input, oid: 'c2' });
		await settle();
		expect(t.calls).toHaveLength(2);
		t.base.follow({ ...input, oid: 'c2', mark: 'deleted' });
		await settle();
		expect(t.calls).toHaveLength(3);
	});

	// Decision: a re-read of the same file keeps what is shown until the answer, so the view is not made again for an equal text.
	it('keeps the text shown while the same file is read again, and says nothing for an equal answer', async () => {
		const t = setup(() => read('same'));
		t.base.follow({ path: 'a.kt', oid: 'c1' });
		await settle();
		const seen: string[] = [];
		t.base.subscribe(() => seen.push(t.base.view.kind));
		t.base.follow({ path: 'a.kt', oid: 'c2' });
		expect(t.base.view.kind).toBe('text');
		await settle();
		expect(seen).toEqual([]);
		t.base.follow({ path: 'b.kt', oid: 'c2' }); // another file starts from loading
		expect(t.base.view.kind).toBe('loading');
	});

	// Decision: a file HEAD does not hold (new, untracked) compares against nothing: an empty text, not a message.
	it('takes a file HEAD does not hold as an empty text', async () => {
		const t = setup(() => read(undefined));
		t.base.follow({ path: 'new.kt', mark: 'untracked' });
		await settle();
		expect(t.base.view).toEqual({ kind: 'text', text: '', exists: false });
	});

	// Decision: binary, too large, and a refusal are messages, never text.
	it('turns binary, too large and an error into messages', async () => {
		const bin = setup(() => read(undefined, { exists: true, binary: true }));
		bin.base.follow({ path: 'a.png' });
		await settle();
		expect(bin.base.view).toEqual({ kind: 'message', text: BINARY_TEXT });
		const big = setup(() => read(undefined, { exists: true, tooLarge: true }));
		big.base.follow({ path: 'big.txt' });
		await settle();
		expect(big.base.view).toEqual({ kind: 'message', text: TOO_LARGE_TEXT });
		const err = setup(() => capError('GIT_NOT_A_REPOSITORY', 'not a repository'));
		err.base.follow({ path: 'a.kt' });
		await settle();
		expect(err.base.view.kind).toBe('message');
		expect((err.base.view as { text: string }).text).toContain('GIT_NOT_A_REPOSITORY');
	});

	// Decision: an answer for a file the panel has moved off is ignored.
	it('ignores an answer that a newer read overtook', async () => {
		const pending: ((r: unknown) => void)[] = [];
		const client = { call: () => new Promise((res) => pending.push(res)) } as unknown as BaseClient;
		const base = new HeadBase(client, { root: '/p' });
		base.follow({ path: 'a.kt' });
		base.follow({ path: 'b.kt' });
		pending[1]!(read('B'));
		await settle();
		pending[0]!(read('A'));
		await settle();
		expect(base.view).toEqual({ kind: 'text', text: 'B', exists: true });
		base.follow(null);
		expect(base.view.kind).toBe('idle');
	});
});
