// Leaving the project with unsaved text (plan m2i D7): the router's `go` (the projects panel
// and "Open folder…") asks before the address changes, and the address changes the page did
// not make (Back, Forward, an edited fragment) are put back and then asked about. The page
// shows `shown`, which only moves when the answer lets it.
import type { UnsavedGuard } from '../editor/unsaved';
import type { Route, Router } from './route';

const same = (a: Route, b: Route): boolean => (a.view === 'project' ? b.view === 'project' && a.workspace === b.workspace : b.view === 'none');

export class Navigation {
	#router: Pick<Router, 'route' | 'go' | 'replace'>;
	#guard: Pick<UnsavedGuard, 'hasDirty' | 'confirm'>;
	#shown: Route;

	constructor(router: Pick<Router, 'route' | 'go' | 'replace'>, guard: Pick<UnsavedGuard, 'hasDirty' | 'confirm'>) {
		this.#router = router;
		this.#guard = guard;
		this.#shown = router.route;
	}

	/** The route the page shows. */
	get shown(): Route {
		return this.#shown;
	}

	/** A navigation the page chose: made now, or after the question. */
	go(target: Route): void {
		if (same(target, this.#shown)) return;
		this.#guard.confirm(() => this.#router.go(target));
	}

	/** The address changed (the router's listener calls this); returns the route to show. */
	onAddress(): Route {
		const next = this.#router.route;
		if (!same(next, this.#shown) && this.#guard.hasDirty()) {
			// The browser has moved already: put the address back, and move it again if the answer lets it.
			this.#router.replace(this.#shown);
			this.#guard.confirm(() => this.#router.replace(next));
			return this.#shown;
		}
		this.#shown = next;
		return next;
	}
}
