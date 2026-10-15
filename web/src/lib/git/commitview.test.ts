import { render } from 'svelte/server';
import { describe, expect, it } from 'vitest';
import CommitView from './CommitView.svelte';
import { commitFileOf } from './diffsource';
import { showOf } from './historytesting';

const C = 'c'.repeat(40);
const P = '1'.repeat(40);

function body() {
	const show = showOf(C, {
		parents: [P, '2'.repeat(40)],
		files: [
			{ path: 'src/A.kt', status: 'modified', added: 1, removed: 1, binary: false },
			{ path: 'src/N.kt', origPath: 'src/O.kt', status: 'renamed', binary: false },
		],
	});
	const html = render(CommitView, {
		props: { expanded: { key: C, oid: C, root: '.', state: 'ready', show, problem: null }, height: 200, onclose: () => {}, onparent: () => {}, onretry: () => {}, onopenfile: () => {} },
	}).body;
	return { html, show };
}

describe('the expanded commit’s file rows (plan m2n step 9)', () => {
	// Decision: a changed file is something to open: its row keeps data-file and holds a button, which is what the keyboard reaches.
	it('keeps data-file on each row and gives it a button that opens the file', () => {
		const { html } = body();
		expect(html).toContain('data-file="src/A.kt"');
		expect(html).toContain('data-file="src/N.kt"');
		expect(html.match(/data-open/g)).toHaveLength(2);
	});

	// Decision: the file opens against the commit's first parent, a rename at its origPath (the Diff tab reads it there).
	it('hands the Diff tab the first parent and a rename’s origPath', () => {
		const { show } = body();
		const renamed = commitFileOf(show, show.files[1]!);
		expect(renamed).toMatchObject({ oid: C, parent: P, path: 'src/N.kt', origPath: 'src/O.kt', status: 'renamed' });
	});
});
