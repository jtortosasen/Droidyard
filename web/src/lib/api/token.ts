// The per-launch token, from the address bar to sessionStorage (http-websocket.md
// §Security baseline, "Bootstrapping the browser"). `droidyard ui` opens
// `/#token=<token>`; a fragment is never sent to a server or into Referer. The token is
// moved into sessionStorage, scoped to this origin and tab, and the fragment is stripped
// from the address bar so a copied URL or a bookmark never carries it.

const KEY = 'droidyard.token';

/** The slice of `window` the bootstrap touches, so a test can pass fakes. */
export interface TokenEnv {
	location: { hash: string; pathname: string; search: string };
	history: { replaceState(data: unknown, unused: string, url?: string | URL | null): void };
	storage: Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>;
}

/**
 * The token this tab has, or null when it has none and must show the "run
 * `droidyard ui`" instruction. A token in the fragment wins over a stored one (a new
 * launch has a new token) and is stripped from the address bar. A fragment that carries
 * no token is left alone.
 */
export function bootstrapToken(env: TokenEnv): string | null {
	const fromHash = new URLSearchParams(env.location.hash.replace(/^#/, '')).get('token');
	if (fromHash) {
		try {
			env.storage.setItem(KEY, fromHash);
		} catch {
			// Storage can be blocked: the token then lives for this page load only.
		}
		env.history.replaceState(null, '', env.location.pathname + env.location.search);
		return fromHash;
	}
	try {
		return env.storage.getItem(KEY) || null;
	} catch {
		return null;
	}
}

/** Forgets the stored token: the daemon refused it (close code 4401), so a reload must not retry it. */
export function forgetToken(storage: Pick<Storage, 'removeItem'>): void {
	try {
		storage.removeItem(KEY);
	} catch {
		// Nothing stored that could be read back either.
	}
}
