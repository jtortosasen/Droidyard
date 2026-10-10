// The Logcat panel's model: the stream, the per-frame batcher, the ring, the filtered
// view and the follow / pause state, with no markup and no framework (plan m2a D7), so a
// test drives it with a fake client and a fake frame scheduler. The component only reads
// it and reports what the user did.
//
// How the pieces meet: the stream turns events into entries and hands them to the
// batcher, which holds them until the next animation frame and then moves them into the
// ring in one step; the view indexes the ring's entries that pass the filter; the
// component renders the rows of the view that the scroll position shows. One flush per
// frame means one notification and one render per frame, whatever the event rate.
import { Batcher, browserFrames, HIDDEN_QUEUE_CAP, type FrameScheduler } from './batcher';
import type { Entry } from './entry';
import { CrashMarker } from './crash';
import { Ring, RING_CAPACITY } from './ring';
import { LogStream, type StreamClient, type StreamStatus } from './stream';
import { View } from './view';
import type { ConnectionState } from '../api/client';

export interface SessionOptions {
	client: StreamClient;
	frames?: FrameScheduler;
	capacity?: number;
	hiddenCap?: number;
	hidden?: boolean;
}

export interface Row {
	/** The row's number in the view; stable while older rows are evicted. */
	seq: number;
	entry: Entry;
}

/** Lines the panel does not hold, by who dropped them. */
export interface Drops {
	/** Reported by the daemon (`dropped` of a lines event). */
	daemon: number;
	/** Dropped from the queue while the tab was hidden. */
	queue: number;
	/** Forgotten by the ring at its capacity. */
	ring: number;
}

export class LogcatSession {
	#ring: Ring<Entry>;
	#view: View;
	#batcher: Batcher<Entry>;
	#stream: LogStream;
	#listeners = new Set<() => void>();

	#crash = new CrashMarker();
	#device = '';
	#status: StreamStatus = { state: 'idle', detail: '' };
	#daemonDropped = 0;

	#following = true;
	#frozenEnd: number | null = null; // the view's end while paused
	#anchor = 0; // the view's end when following stopped: what "new lines" counts from
	#lastHead = 0;

	constructor(opts: SessionOptions) {
		this.#ring = new Ring<Entry>(opts.capacity ?? RING_CAPACITY);
		this.#view = new View(this.#ring, () => true);
		this.#batcher = new Batcher<Entry>({
			frames: opts.frames ?? browserFrames,
			flush: (items, dropped) => this.#flush(items, dropped),
			hiddenCap: opts.hiddenCap ?? HIDDEN_QUEUE_CAP,
			hidden: opts.hidden ?? false,
		});
		this.#stream = new LogStream(opts.client, {
			entries: (entries, daemonDropped) => {
				this.#daemonDropped += daemonDropped;
				for (const e of entries) this.#crash.mark(e);
				this.#batcher.push(entries);
			},
			status: (status) => {
				this.#status = status;
				this.#notify();
			},
		});
	}

	// What the component reads.

	get status(): StreamStatus {
		return this.#status;
	}
	get device(): string {
		return this.#device;
	}
	get following(): boolean {
		return this.#following;
	}
	get paused(): boolean {
		return this.#frozenEnd !== null;
	}
	get capacity(): number {
		return this.#ring.capacity;
	}
	/** Entries held in the ring (lines and markers), before the filter. */
	get held(): number {
		return this.#ring.length;
	}
	get drops(): Drops {
		return { daemon: this.#daemonDropped, queue: this.#batcher.dropped, ring: this.#ring.evicted };
	}
	/** Rows the list shows: those of the view, up to where a pause froze it. */
	get count(): number {
		const end = this.#frozenEnd ?? this.#view.endSeq;
		return Math.max(0, end - this.#view.headSeq);
	}
	/** Rows that arrived below what the user is looking at: since following stopped or the pause began. */
	get newCount(): number {
		if (this.#frozenEnd !== null) return Math.max(0, this.#view.endSeq - this.#frozenEnd);
		if (this.#following) return 0;
		return Math.max(0, this.#view.endSeq - this.#anchor);
	}
	get headSeq(): number {
		return this.#view.headSeq;
	}

	/** Rows `[start, end)` of the list, clamped to it. */
	rows(start: number, end: number): Row[] {
		const out: Row[] = [];
		const head = this.#view.headSeq;
		const n = this.count;
		for (let i = Math.max(0, start); i < Math.min(end, n); i++) {
			const entry = this.#view.entry(head + i);
			if (entry !== undefined) out.push({ seq: head + i, entry });
		}
		return out;
	}

	/** How many rows left the front of the list since the last call: the scroll position moves up by that many rows. */
	takeShift(): number {
		const head = this.#view.headSeq;
		const shift = head - this.#lastHead;
		this.#lastHead = head;
		return shift;
	}

	/** Runs `listener` after every change (once per frame while lines arrive). Returns the way to stop it. */
	onChange(listener: () => void): () => void {
		this.#listeners.add(listener);
		return () => this.#listeners.delete(listener);
	}

	// What the user and the connection do.

	/** Streams the whole of `device` (from its newest lines; the UI filters, D40); '' stops. The panel starts empty. */
	select(device: string): void {
		if (device === this.#device) return;
		this.#device = device;
		this.#crash.reset();
		this.clear();
		if (device === '') this.#stream.stop();
		else this.#stream.open(device);
	}

	/** Starts the stream again where it left off, after it stopped. */
	restart(): void {
		this.#stream.resume();
	}

	/** Shows the entries `match` accepts (a compiled query: query.ts); the held ones are tested again. */
	setFilter(match: (e: Entry) => boolean): void {
		this.#view.setFilter(match);
		this.#settle();
		this.#notify();
	}

	setPaused(paused: boolean): void {
		if (paused === this.paused) return;
		if (paused) {
			this.#frozenEnd = this.#view.endSeq;
		} else {
			this.#frozenEnd = null;
			this.#following = true;
			this.#anchor = this.#view.endSeq;
		}
		this.#notify();
	}

	/** The user scrolled to the bottom (true) or away from it (false). */
	setFollowing(following: boolean): void {
		if (following === this.#following) return;
		this.#following = following;
		this.#anchor = this.#view.endSeq;
		this.#notify();
	}

	/** Forgets every held line, the queue and the drop counts; the stream goes on. */
	clear(): void {
		this.#batcher.reset();
		this.#ring.clear();
		this.#view.reset();
		this.#daemonDropped = 0;
		this.#settle();
		this.#notify();
	}

	setHidden(hidden: boolean): void {
		this.#batcher.setHidden(hidden);
	}

	onConnection(state: ConnectionState): void {
		this.#stream.onConnection(state);
	}

	dispose(): void {
		this.#stream.stop();
		this.#batcher.dispose();
		this.#listeners.clear();
	}

	#flush(items: Entry[], dropped: number): void {
		if (dropped > 0) {
			this.#ring.push({
				marker: 'hidden',
				text: `${dropped} entr${dropped === 1 ? 'y was' : 'ies were'} dropped while this tab was in the background`,
			});
		}
		for (const item of items) this.#ring.push(item);
		this.#view.sync();
		if (this.#following && this.#frozenEnd === null) this.#anchor = this.#view.endSeq;
		this.#notify();
	}

	/** After the view was rebuilt or emptied: back to the bottom, the pause (if any) at the new end. */
	#settle(): void {
		this.#following = true;
		this.#anchor = this.#view.endSeq;
		if (this.#frozenEnd !== null) this.#frozenEnd = this.#view.endSeq;
		this.#lastHead = this.#view.headSeq;
	}

	#notify(): void {
		for (const l of [...this.#listeners]) l();
	}
}
