import { describe, expect, it } from 'vitest';
import { parseRoute, routeHash, Router, type RouteEnv } from './route';

function fakeEnv(initial = '') {
	let hash = initial;
	const fns = new Set<() => void>();
	const pushed: string[] = [];
	const replaced: string[] = [];
	const env: RouteEnv = {
		hash: () => hash,
		replace: (h) => {
			replaced.push(h);
			hash = h;
		},
		navigate: (h) => {
			pushed.push(h);
			hash = h;
		},
		onHashChange: (fn) => {
			fns.add(fn);
			return () => fns.delete(fn);
		},
	};
	// The user moves through history: the address changes and the browser says so.
	const user = (h: string) => {
		hash = h;
		for (const f of [...fns]) f();
	};
	return { env, pushed, replaced, user, listeners: fns };
}

describe('parseRoute', () => {
	// Decision (ADR-0045 §2, ADR-0047): `w` names the project; without it the page decides (ADR-0047).
	it('reads the project from w and no project from no w', () => {
		expect(parseRoute('#w=abc')).toEqual({ view: 'project', workspace: 'abc' });
		expect(parseRoute('')).toEqual({ view: 'none' });
		expect(parseRoute('#')).toEqual({ view: 'none' });
		expect(parseRoute('#w=')).toEqual({ view: 'none' });
		expect(parseRoute('#other=1')).toEqual({ view: 'none' });
	});

	it('round-trips an id through the fragment', () => {
		const r = { view: 'project', workspace: 'a b&c=d' } as const;
		expect(parseRoute(routeHash(r))).toEqual(r);
		expect(routeHash({ view: 'none' })).toBe('');
	});
});

describe('Router', () => {
	// Decision: the route is the address; history and edits are followed.
	it('follows the browser moving through history', () => {
		const t = fakeEnv('#w=a');
		const router = new Router(t.env);
		const seen: string[] = [];
		router.subscribe(() => seen.push(JSON.stringify(router.route)));
		t.user('#w=b');
		t.user('');
		expect(seen).toEqual(['{"view":"project","workspace":"b"}', '{"view":"none"}']);
	});

	it('navigates with a history entry and tells listeners once even if the browser echoes', () => {
		const t = fakeEnv('');
		const router = new Router(t.env);
		let n = 0;
		router.subscribe(() => n++);
		router.go({ view: 'project', workspace: 'x' });
		t.user('#w=x'); // the hashchange the browser fires after location.hash is assigned
		expect(t.pushed).toEqual(['#w=x']);
		expect(n).toBe(1);
		expect(router.route).toEqual({ view: 'project', workspace: 'x' });
	});

	// Decision (ADR-0047 §2): the redirect to the newest project and the refused `w` replace
	// the history entry; a push would make Back return to the address that is redirected away.
	it('replaces the address without a history entry and tells listeners', () => {
		const t = fakeEnv('');
		const router = new Router(t.env);
		let n = 0;
		router.subscribe(() => n++);
		router.replace({ view: 'project', workspace: 'x' });
		expect(t.replaced).toEqual(['#w=x']);
		expect(t.pushed).toEqual([]);
		expect(n).toBe(1);
		expect(router.route).toEqual({ view: 'project', workspace: 'x' });
	});

	it('stops listening to the browser when the last listener leaves', () => {
		const t = fakeEnv('');
		const router = new Router(t.env);
		const off = router.subscribe(() => {});
		expect(t.listeners.size).toBe(1);
		off();
		expect(t.listeners.size).toBe(0);
	});
});
