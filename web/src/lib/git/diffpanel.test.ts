import { render } from 'svelte/server';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { HeadBase } from './base';
import DiffPanel from './DiffPanel.svelte';
import { DiffSource, type CommitFile, type CommitText, type CommitTextView } from './diffsource';
import type { TabsModel } from '../editor/tabs';
import type { ViewerSet } from '../editor/viewer';
import type { GitWatch } from './watch';

// The Diff tab's header as the server-side renderer draws it (no browser: effects do not run).
// What it states is what the level-4 checks read: the source, the commit and its parent in full, the path.
afterEach(() => vi.unstubAllGlobals());

const C = 'c'.repeat(40);
const P = '1'.repeat(40);
const file = (over: Partial<CommitFile> = {}): CommitFile => ({ root: '.', oid: C, parent: P, path: 'src/A.kt', status: 'modified', binary: false, ...over });

function panel(source: DiffSource, active: string | null, cview: CommitTextView = { kind: 'loading' }) {
	vi.stubGlobal('window', {});
	const model = (view: unknown, extra: object = {}) => ({ view, subscribe: () => () => {}, ...extra });
	return render(DiffPanel, {
		props: {
			tabs: model({ tabs: [], active }) as unknown as TabsModel,
			viewers: { get: () => null } as unknown as ViewerSet,
			git: model({ state: 'idle', problem: null, head: null, entries: [], truncated: false }) as unknown as GitWatch,
			base: model({ kind: 'idle' }, { follow: () => {} }) as unknown as HeadBase,
			source,
			commitText: model(cview, { follow: () => {} }) as unknown as CommitText,
		},
	}).body;
}

describe('the Diff tab’s header', () => {
	// Decision (plan m2n step 9): a commit's file says so, with both oids in full for the checks and the short ones for the reader.
	it('states the commit source, the commit, its parent and the path', () => {
		const source = new DiffSource();
		source.showCommit(file());
		const body = panel(source, 'other/Open.kt');
		expect(body).toContain('data-diff-source="commit"');
		expect(body).toContain(`data-commit="${C}"`);
		expect(body).toContain(`data-parent="${P}"`);
		expect(body).toContain('data-diff-path="src/A.kt"');
		expect(body).toContain('src/A.kt · ccccccc vs 1111111');
		expect(body).not.toContain('vs HEAD');
	});

	// Decision: a root commit has no parent to state; the attribute is absent, not empty.
	it('leaves data-parent out for a root commit', () => {
		const source = new DiffSource();
		source.showCommit(file({ parent: null, status: 'added' }));
		const body = panel(source, null);
		expect(body).toContain('data-diff-source="commit"');
		expect(body).not.toContain('data-parent=');
	});

	// Decision: the working tree's header is as m2l made it, now naming its source and path; a commit's text does not leak into it.
	it('keeps the working tree’s header and says it is the working tree', () => {
		const body = panel(new DiffSource(), 'src/A.kt');
		expect(body).toContain('data-diff-source="worktree"');
		expect(body).toContain('data-diff-path="src/A.kt"');
		expect(body).toContain('vs HEAD');
		expect(body).not.toContain('data-commit=');
	});

	// Decision: a deleted file is the right pane alone, so the mode toggle is disabled and Review is the one pressed.
	it('disables the mode toggle for a file the commit deleted', () => {
		const source = new DiffSource();
		source.showCommit(file({ status: 'deleted' }));
		const body = panel(source, null, { kind: 'text', before: 'x', after: null });
		expect(body).toMatch(/data-mode="classic"[^>]*disabled/);
		expect(body).toMatch(/aria-pressed="true"[^>]*data-mode="review"|data-mode="review"[^>]*aria-pressed="true"/);
	});

	// Decision: a binary file or a refusal is a message in place of the view, with the header still naming the file.
	it('shows a message for a file that has no text', () => {
		const source = new DiffSource();
		source.showCommit(file({ binary: true }));
		const body = panel(source, null, { kind: 'message', text: 'This file is binary; there is no text to compare.' });
		expect(body).toContain('This file is binary; there is no text to compare.');
		expect(body).toContain('data-diff-path="src/A.kt"');
		expect(body).not.toContain('data-diff="');
	});
});
