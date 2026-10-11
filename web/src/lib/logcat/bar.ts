// The query bar's model (plan m2e D38): the text as typed, applied to the session on every
// input, with the last valid query kept while the text has an error. A valid query is
// remembered per project under `droidyard.ui.logcat.<workspaceId>` (m2d D25); a storage
// that throws or holds nothing leaves the default.
import type { KeyValueStorage } from '../ui/layout';
import { parseQuery } from './query';
import type { Entry } from './entry';

export const DEFAULT_QUERY = 'package:mine';

export const queryKey = (workspaceId: string): string => `droidyard.ui.logcat.${workspaceId}`;

/** The remembered query, or the default; never throws. */
export function loadQuery(storage: KeyValueStorage | null, workspaceId: string): string {
	try {
		const v = storage?.getItem(queryKey(workspaceId));
		return typeof v === 'string' ? v : DEFAULT_QUERY;
	} catch {
		return DEFAULT_QUERY;
	}
}

function saveQuery(storage: KeyValueStorage | null, workspaceId: string, text: string): void {
	try {
		storage?.setItem(queryKey(workspaceId), text);
	} catch {
		// Not remembered; the query in use is still right.
	}
}

export interface BarSession {
	setFilter(match: (e: Entry) => boolean): void;
}

export interface QueryError {
	message: string;
	/** 0-based offset of the problem in the text. */
	at: number;
}

export interface BarView {
	text: string;
	error: QueryError | null;
	/** The query applied has a `package:mine` term. */
	usesMine: boolean;
}

export interface BarOptions {
	session: BarSession;
	/** The workspace's application ids (D39), read live at every test. */
	mine: ReadonlySet<string>;
	storage: KeyValueStorage | null;
	workspaceId: string;
	now?: () => number;
}

export class QueryBar {
	#o: BarOptions;
	#text: string;
	#error: QueryError | null = null;
	#usesMine = false;

	constructor(o: BarOptions) {
		this.#o = o;
		this.#text = loadQuery(o.storage, o.workspaceId);
		this.#apply();
	}

	get view(): BarView {
		return { text: this.#text, error: this.#error, usesMine: this.#usesMine };
	}

	/** The text now in the input: applied if it parses; if not, the error and the previous filter stay. */
	set(text: string): void {
		this.#text = text;
		this.#apply();
	}

	/** Applies the text again, as the set of ids or the clock changed what it means. */
	reapply(): void {
		this.#apply();
	}

	#apply(): void {
		const r = parseQuery(this.#text, { mine: this.#o.mine, now: this.#o.now });
		if (!r.ok) {
			this.#error = { message: r.error, at: r.at };
			return;
		}
		this.#error = null;
		this.#usesMine = r.usesMine;
		this.#o.session.setFilter(r.predicate);
		saveQuery(this.#o.storage, this.#o.workspaceId, this.#text);
	}
}
