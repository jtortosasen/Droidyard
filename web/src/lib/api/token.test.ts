import { describe, expect, it } from 'vitest';
import { bootstrapToken, forgetToken, type TokenEnv } from './token';

function env(hash: string, stored?: string) {
	const store = new Map<string, string>(stored === undefined ? [] : [['droidyard.token', stored]]);
	const replaced: Array<string | URL | null | undefined> = [];
	const e: TokenEnv = {
		location: { hash, pathname: '/', search: '' },
		history: { replaceState: (_d, _u, url) => void replaced.push(url) },
		storage: {
			getItem: (k) => store.get(k) ?? null,
			setItem: (k, v) => void store.set(k, v),
			removeItem: (k) => void store.delete(k),
		},
	};
	return { e, store, replaced };
}

describe('bootstrapToken', () => {
	it('moves the fragment token into sessionStorage and strips the address bar', () => {
		// The token must not stay in the URL (history, bookmarks, copy-paste): replaceState
		// with the path alone, and the token kept only in this tab's storage.
		const { e, store, replaced } = env('#token=abc123');
		expect(bootstrapToken(e)).toBe('abc123');
		expect(store.get('droidyard.token')).toBe('abc123');
		expect(replaced).toEqual(['/']);
	});

	it('keeps the project id when it strips the token, so a reload stays on the project', () => {
		// ADR-0045 §2: the address ends as `#w=<id>`, the token only in storage.
		const { e, store, replaced } = env('#token=t&w=x');
		expect(bootstrapToken(e)).toBe('t');
		expect(store.get('droidyard.token')).toBe('t');
		expect(replaced).toEqual(['/#w=x']);
	});

	it('keeps the project id whatever the order of the fragment, encoded', () => {
		const { e, replaced } = env('#w=a%20b&token=t');
		bootstrapToken(e);
		expect(replaced).toEqual(['/#w=a+b']);
	});

	it('keeps the query when it strips the fragment', () => {
		const { e, replaced } = env('#token=t');
		e.location.search = '?x=1';
		bootstrapToken(e);
		expect(replaced).toEqual(['/?x=1']);
	});

	it('uses the stored token when there is no fragment, and leaves the address bar alone', () => {
		const { e, replaced } = env('', 'stored');
		expect(bootstrapToken(e)).toBe('stored');
		expect(replaced).toEqual([]);
	});

	it('prefers a new fragment token over a stored one (a new launch has a new token)', () => {
		const { e, store } = env('#token=new', 'old');
		expect(bootstrapToken(e)).toBe('new');
		expect(store.get('droidyard.token')).toBe('new');
	});

	it('has none without a fragment or a stored token: the instruction view', () => {
		expect(bootstrapToken(env('').e)).toBeNull();
		expect(bootstrapToken(env('#other=1').e)).toBeNull();
		expect(bootstrapToken(env('#token=').e)).toBeNull();
	});

	it('still returns the token when storage is blocked', () => {
		const { e } = env('#token=abc');
		e.storage = {
			getItem: () => {
				throw new Error('blocked');
			},
			setItem: () => {
				throw new Error('blocked');
			},
			removeItem: () => {},
		};
		expect(bootstrapToken(e)).toBe('abc');
		expect(bootstrapToken({ ...e, location: { hash: '', pathname: '/', search: '' } })).toBeNull();
	});
});

describe('forgetToken', () => {
	it('removes the stored token so a reload does not retry a refused one', () => {
		const { e, store } = env('', 'stale');
		forgetToken(e.storage);
		expect(store.size).toBe(0);
		expect(bootstrapToken(e)).toBeNull();
	});
});
