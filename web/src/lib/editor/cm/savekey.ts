// Mod-s anywhere in the editor's shadow tree (plan m2i D7, verifier): the keymap saves from the
// text, but the search panel's inputs are outside the content, so the browser would open its own
// Save dialog there. view.ts listens on the shadow root for the same chord.

/** The parts of a keyboard event this decision reads. */
export interface KeyLike {
	key: string;
	metaKey: boolean;
	ctrlKey: boolean;
	altKey: boolean;
	shiftKey: boolean;
	defaultPrevented: boolean;
}

/** Cmd+S or Ctrl+S, plain, and not already handled (the keymap prevents the default when it saves). */
export function isSaveKey(e: KeyLike): boolean {
	return e.key.toLowerCase() === 's' && (e.metaKey || e.ctrlKey) && !e.altKey && !e.shiftKey && !e.defaultPrevented;
}
