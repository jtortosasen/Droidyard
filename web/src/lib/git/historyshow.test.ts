import { describe, expect, it } from 'vitest';
import { settle } from '../build/testing';
import { chain, setup } from './historytesting';

describe('HistoryStore, the expanded commit', () => {
	// Decision (item 10): one git.show per expansion, kept while the row stays loaded; opening it again uses the cache.
	it('makes one git.show per commit opened and reuses it', async () => {
		const t = setup();
		await settle();
		t.store.expand('c0');
		expect(t.store.view.expanded).toMatchObject({ oid: 'c0', state: 'loading' });
		await settle();
		expect(t.showCalls).toEqual([{ oid: 'c0', workspace: '/proj' }]);
		expect(t.store.view.expanded).toMatchObject({ oid: 'c0', state: 'ready' });
		expect(t.store.view.selected).toBe('c0');
		t.store.toggleExpand('c0'); // closes
		expect(t.store.view.expanded).toBeNull();
		t.store.toggleExpand('c0'); // opens again: from the cache
		expect(t.store.view.expanded).toMatchObject({ oid: 'c0', state: 'ready' });
		await settle();
		expect(t.showCalls).toHaveLength(1);
		t.store.expand('c1');
		await settle();
		expect(t.showCalls.map((c) => c.oid)).toEqual(['c0', 'c1']);
		expect(t.store.view.expanded?.oid).toBe('c1'); // at most one open
	});

	// Decision: a slow answer of a row closed or replaced meanwhile is dropped, not shown.
	it('drops the answer of an expansion that was replaced', async () => {
		const t = setup();
		await settle();
		t.store.expand('c0');
		t.store.expand('c1');
		await settle();
		expect(t.store.view.expanded?.oid).toBe('c1');
		t.store.expand('c0');
		t.store.collapse();
		await settle();
		expect(t.store.view.expanded).toBeNull();
	});

	// Decision: a failed git.show is shown as a problem and is not cached, so a retry calls again.
	it('shows a failed read and retries it', async () => {
		const t = setup();
		await settle();
		t.failShowWith(new Error('boom'));
		t.store.expand('c0');
		await settle();
		expect(t.store.view.expanded?.state).toBe('error');
		t.failShowWith(null);
		t.store.expand('c0');
		await settle();
		expect(t.store.view.expanded?.state).toBe('ready');
		expect(t.showCalls).toHaveLength(2);
	});

	// Decision: a row that is not loaded cannot be opened.
	it('ignores an oid that is not loaded', async () => {
		const t = setup(() => chain(5));
		await settle();
		t.store.expand('zz');
		expect(t.store.view.expanded).toBeNull();
		expect(t.showCalls).toHaveLength(0);
	});
});
