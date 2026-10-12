import { describe, expect, it } from 'vitest';
import { UnsavedGuard, type UnsavedSource } from '../editor/unsaved';
import { Navigation } from './navigate';
import { parseRoute, Router, type Route, type RouteEnv } from './route';

function fakeEnv(initial: string) {
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
	// The user presses Back: the address changes and the browser says so.
	const user = (h: string) => {
		hash = h;
		for (const f of [...fns]) f();
	};
	return { env, pushed, replaced, user, hash: () => hash };
}

function setup(dirty: boolean) {
	const t = fakeEnv('#w=a');
	const router = new Router(t.env);
	const state = { dirty, discarded: 0 };
	const source: UnsavedSource = {
		dirtyPaths: () => (state.dirty ? ['a.kt'] : []),
		saveAll: async () => {
			state.dirty = false;
			return null;
		},
		discard: () => {
			state.dirty = false;
			state.discarded++;
		},
	};
	const guard = new UnsavedGuard();
	guard.attach(source);
	const nav = new Navigation(router, guard);
	// What App.svelte does: the router's listener asks the navigation which route to show.
	let route: Route = nav.shown;
	router.subscribe(() => (route = nav.onAddress()));
	return { t, router, guard, nav, state, route: () => route };
}

describe('Navigation (D7)', () => {
	it('goes at once with nothing unsaved, and not at all to the project already shown', () => {
		const s = setup(false);
		s.nav.go({ view: 'project', workspace: 'b' });
		expect(s.t.pushed).toEqual(['#w=b']);
		expect(s.route()).toEqual({ view: 'project', workspace: 'b' });
		s.nav.go({ view: 'project', workspace: 'b' });
		expect(s.t.pushed).toEqual(['#w=b']);
	});

	// Decision: the projects panel's open path asks while a buffer is dirty, and the address does not move until the answer.
	it('asks before the address changes; Cancel leaves it, Don’t save moves it', async () => {
		const s = setup(true);
		s.nav.go({ view: 'project', workspace: 'b' });
		expect(s.t.pushed).toEqual([]);
		expect(s.guard.pending?.paths).toEqual(['a.kt']);
		await s.guard.answer('cancel');
		expect(s.t.pushed).toEqual([]);
		expect(s.route()).toEqual({ view: 'project', workspace: 'a' });

		s.nav.go({ view: 'none' });
		await s.guard.answer('discard');
		expect(s.t.pushed).toEqual(['']);
		expect(s.route()).toEqual({ view: 'none' });
		expect(s.state.discarded).toBe(1);
	});

	// Decision: Back and Forward have happened already: the URL is restored, the dialog asks,
	// and the move is made only by Save or Don't save.
	it('restores the address after Back and asks; Cancel keeps the project, Save follows Back', async () => {
		const s = setup(true);
		s.t.user('#w=b');
		expect(s.t.hash()).toBe('#w=a');
		expect(s.route()).toEqual({ view: 'project', workspace: 'a' });
		expect(s.guard.pending).not.toBeNull();
		await s.guard.answer('cancel');
		expect(s.t.hash()).toBe('#w=a');
		expect(s.route()).toEqual({ view: 'project', workspace: 'a' });

		s.state.dirty = true;
		s.t.user('');
		expect(s.t.hash()).toBe('#w=a');
		await s.guard.answer('save');
		expect(s.t.hash()).toBe('');
		expect(s.route()).toEqual({ view: 'none' });
	});

	it('lets Back through when nothing is unsaved', () => {
		const s = setup(false);
		s.t.user('#w=b');
		expect(s.route()).toEqual({ view: 'project', workspace: 'b' });
		expect(s.guard.pending).toBeNull();
		expect(parseRoute(s.t.hash())).toEqual({ view: 'project', workspace: 'b' });
	});
});
