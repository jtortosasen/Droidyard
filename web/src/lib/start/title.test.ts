import { describe, expect, it } from 'vitest';
import type { ProjectRow } from '../projects/projects';
import { tabTitle, whereTab } from './title';

const row = (id: string, name: string, root: string, mainRoot?: string): ProjectRow => ({
	id,
	name,
	root,
	lastOpenedAt: '',
	state: 'ready',
	trusted: true,
	open: true,
	...(mainRoot !== undefined ? { mainRoot } : {}),
});

describe('whereTab', () => {
	// Decision: the title names the project, not the worktree (owner's correction to Q4).
	it('names a worktree by its listed main tree, not by its own name', () => {
		const rows = [row('m', 'Shop', '/p/shop/'), row('w', 'feature-x', '/p/shop-wt/x', '/p/shop')];
		const w = whereTab(rows, 'w');
		expect(w).toEqual({ project: 'Shop', tree: 'feature-x', primary: false });
		expect(tabTitle(w)).toBe('Droidyard - Shop');
	});

	it('falls back to the base name of mainRoot when the main tree is not listed', () => {
		const w = whereTab([row('w', 'feature-x', '/p/shop-wt/x', '/p/shop/')], 'w');
		expect(w).toEqual({ project: 'shop', tree: 'feature-x', primary: false });
	});

	it('a main tree is its own project and primary', () => {
		expect(whereTab([row('m', 'Shop', '/p/shop')], 'm')).toEqual({ project: 'Shop', tree: 'Shop', primary: true });
	});

	it('no match gives plain Droidyard', () => {
		expect(whereTab([row('m', 'Shop', '/p/shop')], 'zz')).toBeNull();
		expect(whereTab([], null)).toBeNull();
		expect(tabTitle(null)).toBe('Droidyard');
	});
});
