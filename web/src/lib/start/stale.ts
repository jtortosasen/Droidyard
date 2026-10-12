// A daemon that restarted while the tab holds unsaved text (plan m2i D7, verifier). The notice
// "run droidyard ui" replaces the page, and with it the project's models: the text would be gone.
// With unsaved buffers the project stays mounted and the notice is a banner, so the text can
// be copied out (saving is impossible with the old token); without, the notice replaces the page.
import type { ConnectionState } from '../api/client';
import type { UnsavedGuard } from '../editor/unsaved';

/** 'none': the session is fine; 'solo': the notice alone; 'banner': the notice over the project. */
export type StaleMode = 'none' | 'solo' | 'banner';

export class StaleSession {
	#guard: Pick<UnsavedGuard, 'hasDirty'>;
	#mode: StaleMode = 'none';

	constructor(guard: Pick<UnsavedGuard, 'hasDirty'>) {
		this.#guard = guard;
	}

	/**
	 * The mode for the connection state. It is decided when the token is first refused and kept
	 * until the connection is anything else: once the project is gone there is nothing to ask.
	 */
	decide(connection: ConnectionState): StaleMode {
		if (connection !== 'unauthorized') {
			this.#mode = 'none';
		} else if (this.#mode === 'none') {
			this.#mode = this.#guard.hasDirty() ? 'banner' : 'solo';
		}
		return this.#mode;
	}
}
