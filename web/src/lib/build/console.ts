// The console of the last build (plan m2b D14): `artifact.read` of its `consoleRef` over
// the WebSocket, the same primitive an agent uses, the tail first and "Earlier" paging
// back to the start. While any client's build runs it shows "after the build": 2b has no
// live console. The text is shown as text, never as markup.
import type { Params, Result } from '../api/capabilities.gen';
import type { CallOptions } from '../api/client';
import { failure, type Failure } from './failure';
import type { BuildWorkspace } from './build';

/** The tail first read and the size of each "Earlier" page: the capability's largest limit. */
export const PAGE = 262144;

export interface ConsoleSource {
	readonly view: {
		open: boolean;
		last: { buildId: string; consoleRef: string } | null;
		mirror: { building: unknown | null };
	};
	subscribe(listener: () => void): () => void;
}

export interface ConsoleClient {
	call(name: 'artifact.read', params: Params<'artifact.read'>, opts?: CallOptions): Promise<Result<'artifact.read'>>;
}

export interface ConsoleView {
	/** A build is running or queued in this workspace: the console shows after it. */
	building: boolean;
	/** There is a console to show (a finished build exists). */
	hasBuild: boolean;
	text: string;
	/** Where the text shown starts in the console; 0 means the start. */
	offset: number | null;
	size: number | null;
	growing: boolean;
	/** The part of the console before `offset` is not shown yet. */
	hasEarlier: boolean;
	loading: boolean;
	error: Failure | null;
}

export class ConsoleModel {
	#client: ConsoleClient;
	#source: ConsoleSource;
	#ws: BuildWorkspace;
	#listeners = new Set<() => void>();
	#view: ConsoleView | null = null;
	#off: () => void;

	#key: string | null = null; // the build shown: its id and ref
	#ref: string | null = null;
	#text = '';
	#offset: number | null = null;
	#size: number | null = null;
	#growing = false;
	#loading = false;
	#error: Failure | null = null;
	#gen = 0;

	constructor(client: ConsoleClient, source: ConsoleSource, ws: BuildWorkspace) {
		this.#client = client;
		this.#source = source;
		this.#ws = ws;
		this.#off = source.subscribe(() => this.#sourceChanged());
		this.#sourceChanged();
	}

	get view(): ConsoleView {
		const s = this.#source.view;
		this.#view ??= {
			building: s.open && s.mirror.building !== null,
			hasBuild: this.#ref !== null,
			text: this.#text,
			offset: this.#offset,
			size: this.#size,
			growing: this.#growing,
			hasEarlier: this.#offset !== null && this.#offset > 0,
			loading: this.#loading,
			error: this.#error,
		};
		return this.#view;
	}

	subscribe(listener: () => void): () => void {
		this.#listeners.add(listener);
		return () => {
			this.#listeners.delete(listener);
		};
	}

	dispose(): void {
		this.#off();
		this.#gen++;
	}

	/** The tail of the console. */
	async loadTail(): Promise<void> {
		const ref = this.#ref;
		if (ref === null) return;
		const gen = ++this.#gen;
		this.#loading = true;
		this.#error = null;
		this.#changed();
		try {
			const r = await this.#client.call('artifact.read', { ref, offset: -PAGE, limit: PAGE }, { workspace: this.#ws.root.trim() });
			if (gen !== this.#gen) return;
			this.#text = r.text ?? '';
			this.#offset = r.offset ?? 0;
			this.#size = r.size ?? null;
			this.#growing = r.growing === true;
		} catch (e) {
			if (gen !== this.#gen) return;
			this.#error = failure(e);
		}
		this.#loading = false;
		this.#changed();
	}

	/** The page before the text shown: from the previous offset minus the limit, never below 0. */
	async earlier(): Promise<void> {
		const ref = this.#ref;
		const end = this.#offset;
		if (ref === null || end === null || end <= 0 || this.#loading) return;
		const start = Math.max(0, end - PAGE);
		const gen = ++this.#gen;
		this.#loading = true;
		this.#error = null;
		this.#changed();
		try {
			// limit is at least the capability's minimum (4 bytes) and ends at the text shown.
			const r = await this.#client.call('artifact.read', { ref, offset: start, limit: Math.max(4, end - start) }, { workspace: this.#ws.root.trim() });
			if (gen !== this.#gen) return;
			this.#text = (r.text ?? '') + this.#text;
			// A read landing mid-line starts at the next line: the offset moves, never below 0.
			this.#offset = r.offset ?? start;
			this.#size = r.size ?? this.#size;
		} catch (e) {
			if (gen !== this.#gen) return;
			this.#error = failure(e);
		}
		this.#loading = false;
		this.#changed();
	}

	#sourceChanged(): void {
		const s = this.#source.view;
		const key = s.open && s.last ? `${s.last.buildId}\n${s.last.consoleRef}` : null;
		if (key !== this.#key) {
			this.#key = key;
			this.#ref = key === null ? null : s.last!.consoleRef;
			this.#text = '';
			this.#offset = null;
			this.#size = null;
			this.#growing = false;
			this.#loading = false;
			this.#error = null;
			this.#gen++;
			this.#changed();
			// A build in flight is not read: the console is shown after it.
			if (this.#ref !== null && s.mirror.building === null) void this.loadTail();
			return;
		}
		// The build ended and the key did not change (the same last build): read it once.
		if (this.#ref !== null && s.mirror.building === null && this.#offset === null && !this.#loading && this.#error === null) {
			void this.loadTail();
		}
		this.#changed();
	}

	#changed(): void {
		this.#view = null;
		for (const l of [...this.#listeners]) l();
	}
}
