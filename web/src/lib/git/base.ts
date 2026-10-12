// HEAD's text of the file the Diff tab shows (plan m2l step 5; ADR-0067 §1, §2). `git.read`
// answers with the file's whole text at HEAD; this model asks for it and says what to show:
// the text, or a message (a binary or too-large file, a refusal). The browser diffs it with
// the buffer.
//
// It reads again when the file, the path it is read at (a rename's `origPath`), the file's
// status entry or HEAD's commit changes, and not otherwise: the same input twice asks nothing.
// A read that a newer one overtook is ignored, and an answer equal to what is shown changes nothing.
import type { Params, Result } from '../api/capabilities.gen';
import type { CallOptions } from '../api/client';
import { failure, failureText } from '../build/failure';
import type { Mark } from './changes';

export interface BaseClient {
	call(name: 'git.read', params: Params<'git.read'>, opts?: CallOptions): Promise<Result<'git.read'>>;
}

export interface BaseWorkspace {
	readonly root: string;
}

/** What the Diff tab compares the buffer with. */
export type BaseView =
	| { kind: 'idle' }
	| { kind: 'loading' }
	/** `text` is HEAD's; `exists` is false for a file HEAD does not hold (new, untracked): its text is then empty. */
	| { kind: 'text'; text: string; exists: boolean }
	| { kind: 'message'; text: string };

/** The file to read: its path now, how git sees it, and the commit HEAD points at. */
export interface BaseInput {
	path: string;
	mark?: Mark;
	/** For a renamed path: the path HEAD holds it at, which is the one read. */
	origPath?: string;
	oid?: string;
}

export const TOO_LARGE_TEXT = 'The file at HEAD is over 512 KiB, which the editor does not compare.';
export const BINARY_TEXT = 'The file at HEAD is binary; there is no text to compare.';

const IDLE: BaseView = { kind: 'idle' };

export class HeadBase {
	#client: BaseClient;
	#ws: BaseWorkspace;
	#listeners = new Set<() => void>();
	#view: BaseView = IDLE;
	#key: string | null = null;
	#path: string | null = null;
	#ticket = 0;

	constructor(client: BaseClient, ws: BaseWorkspace) {
		this.#client = client;
		this.#ws = ws;
	}

	get view(): BaseView {
		return this.#view;
	}

	subscribe(listener: () => void): () => void {
		this.#listeners.add(listener);
		return () => {
			this.#listeners.delete(listener);
		};
	}

	/** Points the model at a file, or at none (`null`). The same input again changes nothing. */
	follow(input: BaseInput | null): void {
		if (input === null) {
			this.#key = null;
			this.#path = null;
			this.#ticket++;
			this.#set(IDLE);
			return;
		}
		const key = JSON.stringify([input.path, input.mark ?? null, input.origPath ?? null, input.oid ?? null]);
		if (key === this.#key) return;
		this.#key = key;
		const ticket = ++this.#ticket;
		// The same file read again (HEAD or its entry moved): what is shown stays until the answer, so the
		// view on it is not made again for a text that did not change. Another file starts from loading.
		if (input.path !== this.#path || this.#view.kind !== 'text') this.#set({ kind: 'loading' });
		this.#path = input.path;
		const at = input.origPath ?? input.path;
		this.#client.call('git.read', { path: at }, { workspace: this.#ws.root.trim() }).then(
			(r) => {
				if (ticket !== this.#ticket) return;
				if (r.binary) this.#set({ kind: 'message', text: BINARY_TEXT });
				else if (r.tooLarge) this.#set({ kind: 'message', text: TOO_LARGE_TEXT });
				else this.#set({ kind: 'text', text: r.exists ? (r.text ?? '') : '', exists: r.exists });
			},
			(e: unknown) => {
				if (ticket !== this.#ticket) return;
				this.#set({ kind: 'message', text: failureText(failure(e)) });
			},
		);
	}

	dispose(): void {
		this.#ticket++;
		this.#listeners.clear();
	}

	#set(v: BaseView): void {
		const old = this.#view;
		if (old.kind === 'text' && v.kind === 'text' && old.text === v.text && old.exists === v.exists) return;
		this.#view = v;
		for (const l of [...this.#listeners]) l();
	}
}
