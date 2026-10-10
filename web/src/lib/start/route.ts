// Which view the tab shows, from the address bar's fragment (ADR-0045 §2): `#w=<id>` is the
// project with that workspace id; no `w` names nothing, and the page decides (ADR-0047 §2:
// the newest open project, else the projects panel). The id is a name, not a secret, and
// is never sent to a server (a fragment); the token is not in the fragment by now (token.ts).

export type Route = { view: 'none' } | { view: 'project'; workspace: string };

export function parseRoute(hash: string): Route {
	const w = new URLSearchParams(hash.replace(/^#/, '')).get('w');
	return w ? { view: 'project', workspace: w } : { view: 'none' };
}

/** The fragment that shows a project; no project is the empty fragment. */
export function routeHash(route: Route): string {
	return route.view === 'project' ? `#${new URLSearchParams({ w: route.workspace }).toString()}` : '';
}

/** The slice of the browser the router touches, so a test can pass a fake. */
export interface RouteEnv {
	hash(): string;
	/** Moves the address to `hash` as a new history entry ('' is no project). */
	navigate(hash: string): void;
	/** Moves the address to `hash` in place of the current history entry. */
	replace(hash: string): void;
	/** Calls `fn` when the user moves through history or edits the fragment. */
	onHashChange(fn: () => void): () => void;
}

export function browserRouteEnv(win: Window): RouteEnv {
	return {
		hash: () => win.location.hash,
		navigate: (hash) => {
			if (hash === '') win.history.pushState(null, '', win.location.pathname + win.location.search);
			else win.location.hash = hash;
		},
		replace: (hash) => win.history.replaceState(null, '', win.location.pathname + win.location.search + hash),
		onHashChange: (fn) => {
			win.addEventListener('hashchange', fn);
			return () => win.removeEventListener('hashchange', fn);
		},
	};
}

export class Router {
	#env: RouteEnv;
	#listeners = new Set<() => void>();
	#off: (() => void) | null = null;
	#last: string;

	constructor(env: RouteEnv) {
		this.#env = env;
		this.#last = key(parseRoute(env.hash()));
	}

	/** Read from the address every time: the address is the state. */
	get route(): Route {
		return parseRoute(this.#env.hash());
	}

	/** Calls `listener` when the route changes, by `go` or by the user. */
	subscribe(listener: () => void): () => void {
		this.#listeners.add(listener);
		this.#off ??= this.#env.onHashChange(() => this.#changed());
		return () => {
			this.#listeners.delete(listener);
			if (this.#listeners.size === 0) {
				this.#off?.();
				this.#off = null;
			}
		};
	}

	go(route: Route): void {
		this.#env.navigate(routeHash(route));
		this.#changed();
	}

	/** Like `go`, without a history entry: a redirect the user did not choose. */
	replace(route: Route): void {
		this.#env.replace(routeHash(route));
		this.#changed();
	}

	#changed(): void {
		const k = key(this.route);
		if (k === this.#last) return;
		this.#last = k;
		for (const l of [...this.#listeners]) l();
	}
}

const key = (r: Route): string => (r.view === 'project' ? `w:${r.workspace}` : 'none');
