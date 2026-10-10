// The build console (plan m2c step 9; ADR-0044): the newest build's output, line by line
// while it runs, and the console of the last finished build otherwise. The text is shown
// as text, never as markup.
//
// While a workspace is open the model keeps one `build.watchOutput` subscription. Its
// result lists the running builds with each console's size; the events carry the lines
// after that, each with the byte `offset` of its first line. For the build on screen the
// model holds `end`, the byte up to which it has the console, and one rule decides every
// event (the result's description is the contract):
//   - a line that starts below `end` is already held: dropped;
//   - an event that starts at `end` is applied;
//   - an event that starts past `end` is preceded by the bytes in between, read with
//     `artifact.read`, then applied.
// `events.gap` and a reconnect (a new subscription) read from `end` to the console's
// present end. Events and reads are applied one at a time, in order, so no line is lost
// or shown twice. Only `end` moves the position, and only forward past bytes that were read.
//
// The panel keeps the newest build's last CONSOLE_LINES lines (D18); the part before the
// first one is paged back with "Earlier" (plan m2b D14). A build that ended before the
// page was opened is read from its end, as 2b did. There is no polling: every read is
// caused by a snapshot, an event that skips ahead, a gap, a reconnect or the user.
import { eventTopics, type EventPayload, type Params, type Result } from '../api/capabilities.gen';
import {
	ConnectionError,
	type CallOptions,
	type ConnectionState,
	type EventFrame,
	type SubscribeHandlers,
	type Subscription,
} from '../api/client';
import { browserFrames, type FrameScheduler } from '../logcat/batcher';
import { failure, failureText, type Failure } from './failure';
import type { BuildWorkspace } from './build';
import { CONSOLE_LINES, LineStore, splitLines, utf8Length, type Row } from './lines';

/** The tail first read and the size of each page: the capability's largest limit. */
export const PAGE = 262144;

/** How many builds the model remembers as older than the one on screen. */
const RETIRED = 16;

const TOPIC_OUTPUT = eventTopics['build.watchOutput'];
const TOPIC_GAP = 'events.gap';

type Lines = EventPayload<'build.watchOutput', 'lines'>;

export interface ConsoleSource {
	readonly view: {
		open: boolean;
		last: { buildId: string; consoleRef: string } | null;
		mirror: { building: unknown | null };
	};
	subscribe(listener: () => void): () => void;
}

export interface ConsoleClient {
	call<N extends 'artifact.read' | 'events.unsubscribe'>(name: N, params: Params<N>, opts?: CallOptions): Promise<Result<N>>;
	subscribe(
		name: 'build.watchOutput',
		params: Params<'build.watchOutput'>,
		handlers: SubscribeHandlers,
		opts?: CallOptions,
	): Promise<Subscription & { readonly result: Result<'build.watchOutput'> }>;
}

export type WatchState = 'idle' | 'starting' | 'watching' | 'waiting' | 'error' | 'ended';

export interface ConsoleView {
	/** There is a console to show. */
	hasBuild: boolean;
	buildId: string | null;
	/** The build on screen is running: its lines arrive as they are written. */
	live: boolean;
	/** Lines held. */
	count: number;
	/** The seq of the first line held. */
	startSeq: number;
	/** Where the first line held starts in the console, in bytes; 0 means the start. */
	offset: number | null;
	size: number | null;
	growing: boolean;
	/** The part of the console before the first line held is not shown yet. */
	hasEarlier: boolean;
	/** Lines forgotten from the front to stay under the cap. */
	evicted: number;
	loading: boolean;
	error: Failure | null;
	watch: { state: WatchState; detail: string };
}

type Item = { kind: 'lines'; offset: number; lines: readonly string[] } | { kind: 'catchup'; to: number };

/** The build on screen. */
interface Shown {
	buildId: string;
	ref: string;
	store: LineStore;
	/** The console is held from `store.startOffset` up to this byte. */
	end: number;
	size: number | null;
	growing: boolean;
	/** The head is read: events and reads may be applied. */
	ready: boolean;
	live: boolean;
	queue: Item[];
	pumping: boolean;
}

export class ConsoleModel {
	#client: ConsoleClient;
	#source: ConsoleSource;
	#ws: BuildWorkspace;
	#frames: FrameScheduler;
	#listeners = new Set<() => void>();
	#view: ConsoleView | null = null;
	#offs: (() => void)[] = [];

	#wsId: string | null = null;
	#shown: Shown | null = null;
	#retired: string[] = [];
	#loading = false;
	#error: Failure | null = null;

	#run: object | null = null;
	#sub: Subscription | null = null;
	#waiting = false;
	#watch: { state: WatchState; detail: string } = { state: 'idle', detail: '' };
	#frame: unknown = null;

	constructor(client: ConsoleClient, source: ConsoleSource, ws: BuildWorkspace, frames: FrameScheduler = browserFrames) {
		this.#client = client;
		this.#source = source;
		this.#ws = ws;
		this.#frames = frames;
		this.#offs.push(source.subscribe(() => this.#sourceChanged()));
		this.#offs.push(ws.subscribe(() => this.#workspaceChanged()));
		this.#workspaceChanged();
		this.#sourceChanged();
	}

	get view(): ConsoleView {
		const sh = this.#shown;
		this.#view ??= {
			hasBuild: sh !== null,
			buildId: sh?.buildId ?? null,
			live: sh?.live ?? false,
			count: sh?.store.count ?? 0,
			startSeq: sh?.store.startSeq ?? 0,
			offset: sh && sh.ready ? sh.store.startOffset : null,
			size: sh?.size ?? null,
			growing: sh?.growing ?? false,
			hasEarlier: sh !== null && sh.ready && sh.store.startOffset > 0,
			evicted: sh?.store.evicted ?? 0,
			loading: this.#loading,
			error: this.#error,
			watch: this.#watch,
		};
		return this.#view;
	}

	/** The rows `start` to `end` (exclusive) of the lines held. */
	rows(start: number, end: number): Row[] {
		return this.#shown?.store.rows(start, end) ?? [];
	}

	subscribe(listener: () => void): () => void {
		this.#listeners.add(listener);
		return () => {
			this.#listeners.delete(listener);
		};
	}

	/** The connection's state, from the client: a watch that lost its session starts again when it is open. */
	onConnection(state: ConnectionState): void {
		if (state === 'open' && this.#waiting) {
			this.#waiting = false;
			this.#begin();
		} else if ((state === 'unauthorized' || state === 'closed') && this.#run !== null) {
			this.#waiting = false;
			this.#setWatch('ended', state === 'unauthorized' ? 'the daemon refused the token' : 'the connection was closed');
		}
	}

	dispose(): void {
		for (const off of this.#offs) off();
		this.#offs = [];
		this.#run = null;
		this.#shown = null;
		this.#release();
		if (this.#frame !== null) this.#frames.cancel(this.#frame);
		this.#frame = null;
	}

	/** The page before the lines held: the bytes before `offset`, ending at a line start. */
	async earlier(): Promise<void> {
		const sh = this.#shown;
		if (sh === null || !sh.ready || this.#loading) return;
		const start = sh.store.startOffset;
		if (start <= 0) return;
		const from = Math.max(0, start - PAGE);
		this.#loading = true;
		this.#error = null;
		this.#changed();
		try {
			// limit is at least the capability's minimum (4 bytes) and ends where the lines held start.
			const r = await this.#client.call('artifact.read', { ref: sh.ref, offset: from, limit: Math.max(4, start - from) });
			if (this.#shown !== sh) return;
			// The live build moved the front meanwhile (older lines were forgotten): the page no longer fits.
			if (sh.store.startOffset !== start) return;
			let text = r.text ?? '';
			// A positive offset may land mid-line: the first piece is a partial line, left to the page before.
			if (from > 0) {
				const i = text.indexOf('\n');
				text = i < 0 ? '' : text.slice(i + 1);
			}
			if ((r.nextOffset ?? start) !== start || text === '') {
				throw new Error('the console could not be read back to the lines shown');
			}
			sh.store.prepend(splitLines(text), start - utf8Length(text));
			sh.size = r.size ?? sh.size;
		} catch (e) {
			if (this.#shown !== sh) return;
			this.#error = failure(e);
		} finally {
			if (this.#shown === sh) {
				this.#loading = false;
				this.#changed();
			}
		}
	}

	// --- the watch -------------------------------------------------------------------

	#workspaceChanged(): void {
		const id = this.#ws.id;
		if (id === this.#wsId) return;
		this.#wsId = id;
		this.#run = null;
		this.#waiting = false;
		this.#release();
		this.#shown = null;
		this.#retired = [];
		this.#loading = false;
		this.#error = null;
		this.#setWatch('idle', '');
		if (id !== null) this.#begin();
		this.#changed();
	}

	#begin(): void {
		const run = {};
		this.#run = run;
		this.#waiting = false;
		this.#release();
		this.#setWatch('starting', '');
		const handlers: SubscribeHandlers = {
			onEvent: (frame) => {
				if (this.#run === run) this.#event(frame);
			},
			onEnd: (why) => {
				if (this.#run !== run) return;
				this.#sub = null;
				if (why.kind === 'lost') {
					this.#waiting = true;
					this.#setWatch('waiting', 'the connection was lost');
				} else {
					this.#setWatch('ended', why.message);
				}
			},
		};
		this.#client.subscribe('build.watchOutput', {}, handlers, { workspace: this.#ws.root.trim() }).then(
			(sub) => {
				if (this.#run !== run) {
					this.#unsubscribe(sub); // replaced or disposed while the answer was on its way
					return;
				}
				this.#sub = sub;
				this.#setWatch('watching', '');
				this.#snapshot(sub.result.builds ?? []);
			},
			(err: unknown) => {
				if (this.#run !== run) return;
				if (err instanceof ConnectionError && err.kind === 'lost') {
					this.#waiting = true;
					this.#setWatch('waiting', 'the connection was lost');
				} else if (err instanceof ConnectionError) {
					this.#setWatch('ended', err.message);
				} else {
					this.#setWatch('error', failureText(failure(err)));
				}
			},
		);
	}

	/** The running builds at the moment the watch opened: the newest is shown, the others are older. */
	#snapshot(builds: readonly { buildId: string; consoleRef: string; offset: number }[]): void {
		const prev = this.#shown;
		if (builds.length === 0) {
			// A build on screen that is gone ended while the watch was down: read what it wrote last.
			if (prev !== null && prev.live) {
				prev.live = false;
				prev.queue.push({ kind: 'catchup', to: Infinity });
				this.#pump(prev);
				this.#changed();
			}
			return;
		}
		for (const b of builds.slice(0, -1)) this.#retire(b.buildId);
		const b = builds[builds.length - 1]!;
		const sh = this.#observe(b.buildId, b.consoleRef, b.offset);
		if (sh !== null && prev === sh) {
			// The same build as before: whatever it wrote while the watch was down is read now.
			sh.queue.push({ kind: 'catchup', to: b.offset });
			this.#pump(sh);
		}
	}

	#event(frame: EventFrame): void {
		const ev = frame.event;
		if (ev.topic === TOPIC_GAP) {
			// Events were skipped, with lines in them: read from where the console is held up to.
			const sh = this.#shown;
			if (sh !== null && sh.live) {
				sh.queue.push({ kind: 'catchup', to: Infinity });
				this.#pump(sh);
			}
			return;
		}
		if (ev.topic !== TOPIC_OUTPUT || ev.type !== 'lines') return;
		const p = ev.payload as Partial<Lines> | null;
		if (!p || typeof p.buildId !== 'string' || typeof p.consoleRef !== 'string' || typeof p.offset !== 'number' || !Array.isArray(p.lines)) {
			return;
		}
		const sh = this.#observe(p.buildId, p.consoleRef, null);
		if (sh === null) return;
		sh.queue.push({ kind: 'lines', offset: p.offset, lines: p.lines });
		this.#pump(sh);
	}

	/**
	 * Makes the build the one on screen if it is not, and says it is running. `snap` is the
	 * console's size when a snapshot listed it; a build first met in an event started after
	 * the watch opened, so its console is held from byte 0.
	 */
	#observe(buildId: string, ref: string, snap: number | null): Shown | null {
		if (this.#retired.includes(buildId)) return null;
		let sh = this.#shown;
		if (sh === null || sh.buildId !== buildId) {
			// The console of the last finished build, if it is on screen, is older than this build.
			const last = this.#source.view.last;
			if (last !== null && last.buildId !== buildId) this.#retire(last.buildId);
			sh = this.#show(buildId, ref);
			if (snap === null) sh.ready = true;
			else void this.#head(sh, snap);
		}
		if (!sh.live) {
			sh.live = true;
			this.#changed();
		}
		return sh;
	}

	// --- the build on screen -----------------------------------------------------------

	#show(buildId: string, ref: string): Shown {
		const prev = this.#shown;
		if (prev !== null) this.#retire(prev.buildId);
		const sh: Shown = {
			buildId,
			ref,
			store: new LineStore(CONSOLE_LINES),
			end: 0,
			size: null,
			growing: false,
			ready: false,
			live: false,
			queue: [],
			pumping: false,
		};
		this.#shown = sh;
		this.#loading = false;
		this.#error = null;
		return sh;
	}

	#retire(buildId: string): void {
		if (this.#retired.includes(buildId)) return;
		this.#retired.push(buildId);
		if (this.#retired.length > RETIRED) this.#retired.shift();
	}

	#sourceChanged(): void {
		const s = this.#source.view;
		const sh = this.#shown;
		if (sh !== null && sh.live && s.mirror.building === null) {
			// The build ended: its console no longer grows, and what is held is its size.
			sh.live = false;
			sh.growing = false;
			sh.size = sh.end;
		}
		const last = s.open ? s.last : null;
		// While a build runs, the last finished one is older than it: never shown over it.
		if (sh !== null && sh.live && last !== null && last.buildId !== sh.buildId) this.#retire(last.buildId);
		if (last !== null && (sh === null || (sh.buildId !== last.buildId && !sh.live && !this.#retired.includes(last.buildId)))) {
			// A build that ended before this page saw it, or the first load: its console, from its end.
			// A build on screen that is this one is kept: it has every line already.
			void this.#loadTail(this.#show(last.buildId, last.consoleRef));
		}
		this.#changed();
	}

	/** The tail of a finished build's console; a negative offset starts at a line. */
	async #loadTail(sh: Shown): Promise<void> {
		this.#loading = true;
		this.#error = null;
		this.#changed();
		try {
			const r = await this.#client.call('artifact.read', { ref: sh.ref, offset: -PAGE, limit: PAGE });
			if (this.#shown !== sh) return;
			const text = r.text ?? '';
			const start = r.offset ?? 0;
			sh.store.clear(start);
			sh.store.append(splitLines(text));
			sh.end = r.nextOffset ?? start + utf8Length(text);
			sh.size = r.size ?? null;
			sh.growing = r.growing === true;
		} catch (e) {
			if (this.#shown !== sh) return;
			this.#error = failure(e);
		} finally {
			if (this.#shown === sh) {
				sh.ready = true;
				this.#loading = false;
				this.#changed();
				this.#pump(sh);
			}
		}
	}

	/**
	 * The head of a running build's console, up to the size a snapshot gave: the whole of it
	 * when small, else its last page, with the rest left to "Earlier".
	 */
	async #head(sh: Shown, snap: number): Promise<void> {
		this.#loading = true;
		this.#error = null;
		this.#changed();
		try {
			if (snap > PAGE) {
				const from = snap - PAGE;
				const r = await this.#client.call('artifact.read', { ref: sh.ref, offset: from, limit: PAGE });
				if (this.#shown !== sh) return;
				// The read starts at a byte, maybe inside a line: the lines start after the first newline.
				const text = r.text ?? '';
				const i = text.indexOf('\n');
				const kept = i < 0 ? '' : text.slice(i + 1);
				const next = r.nextOffset ?? from + utf8Length(text);
				sh.store.clear(next - utf8Length(kept));
				sh.store.append(splitLines(kept));
				sh.end = next;
				sh.size = r.size ?? null;
				sh.growing = r.growing === true;
			} else {
				await this.#fill(sh, snap);
			}
		} catch (e) {
			if (this.#shown !== sh) return;
			// What could not be read stays unread: the lines held start at the snapshot, and
			// "Earlier" reads what is before them.
			this.#error = failure(e);
			sh.store.clear(snap);
			sh.end = snap;
		} finally {
			if (this.#shown === sh) {
				sh.ready = true;
				this.#loading = false;
				this.#changed();
				this.#pump(sh);
			}
		}
	}

	// --- applying events and reads, one at a time, in order ----------------------------

	#pump(sh: Shown): void {
		if (sh.pumping) return;
		sh.pumping = true;
		void (async () => {
			try {
				while (this.#shown === sh && sh.ready && sh.queue.length > 0) {
					await this.#apply(sh, sh.queue.shift()!);
				}
			} finally {
				sh.pumping = false;
			}
		})();
	}

	async #apply(sh: Shown, item: Item): Promise<void> {
		try {
			if (item.kind === 'catchup') {
				if (item.to > sh.end) await this.#fill(sh, item.to);
			} else {
				// The catch-up rule: an event that starts past what is held is preceded by the bytes in between.
				if (item.offset > sh.end) await this.#fill(sh, item.offset);
				if (this.#shown !== sh) return;
				let pos = item.offset;
				const fresh: string[] = [];
				for (const line of item.lines) {
					if (pos > sh.end) break; // the bytes before it are still missing: nothing is applied out of order
					const n = utf8Length(line) + 1;
					// A line that starts below the end is held already: dropped.
					if (pos === sh.end) {
						fresh.push(line);
						sh.end = pos + n;
					}
					pos += n;
				}
				if (fresh.length > 0) {
					sh.store.append(fresh);
					this.#touch();
				}
			}
			if (this.#shown === sh && this.#error !== null) {
				this.#error = null;
				this.#changed();
			}
		} catch (e) {
			// `end` did not move: the next event that skips ahead reads the same bytes again.
			if (this.#shown !== sh) return;
			this.#error = failure(e);
			this.#changed();
		}
	}

	/**
	 * Reads the console from `sh.end` up to `to` (to its present end for Infinity) and adds
	 * its lines. A line longer than a page arrives in pieces and is added once complete.
	 */
	async #fill(sh: Shown, to: number): Promise<void> {
		let pos = sh.end;
		let carry = '';
		const finite = Number.isFinite(to);
		while (pos < to) {
			const limit = finite ? Math.min(PAGE, Math.max(4, to - pos)) : PAGE;
			const r = await this.#client.call('artifact.read', { ref: sh.ref, offset: pos, limit });
			if (this.#shown !== sh) return;
			const text = r.text ?? '';
			const next = r.nextOffset ?? pos + utf8Length(text);
			sh.size = r.size ?? sh.size;
			sh.growing = r.growing === true;
			if (next <= pos) {
				if (finite) throw new Error('the console is shorter than the output reported');
				break;
			}
			const pieces = (carry + text).split('\n');
			const rest = pieces.pop()!;
			// Text after the last newline is the start of a longer line, unless the console is
			// closed and ends there.
			if (rest !== '' && r.truncated !== true) pieces.push(rest);
			carry = r.truncated === true ? rest : '';
			sh.store.append(pieces);
			pos = next;
			sh.end = pos - utf8Length(carry);
			this.#touch();
			if (!finite && r.size !== undefined && pos >= r.size) break;
		}
	}

	// --- notification ------------------------------------------------------------------

	/** The lines changed: told once per animation frame, however many events arrived. */
	#touch(): void {
		if (this.#frame !== null) return;
		this.#frame = this.#frames.request(() => {
			this.#frame = null;
			this.#changed();
		});
	}

	#setWatch(state: WatchState, detail: string): void {
		this.#watch = { state, detail };
		this.#changed();
	}

	#changed(): void {
		this.#view = null;
		for (const l of [...this.#listeners]) l();
	}

	#release(): void {
		const sub = this.#sub;
		this.#sub = null;
		if (sub) this.#unsubscribe(sub);
	}

	#unsubscribe(sub: Subscription): void {
		sub.stop();
		this.#client.call('events.unsubscribe', { subscriptionId: sub.subscriptionId }).catch(() => {
			// The session may be gone, and then so is the subscription.
		});
	}
}
