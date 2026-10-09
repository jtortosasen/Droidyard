// Moves what the stream delivers into the panel once per animation frame, however many
// events arrived in between (plan m2a step 5; D8). A burst of 5,000 lines/s is then
// about 80 lines per flush, one render, not one render per event.
//
// A hidden tab gets no animation frames, so nothing would drain the queue: while the tab
// is hidden the queue is capped, dropping its oldest items and counting them, and the
// next flush after the tab is visible again reports the count. Nothing is lost silently.

/** The slice of requestAnimationFrame the batcher uses; a test passes a fake. */
export interface FrameScheduler {
	request(callback: () => void): unknown;
	cancel(handle: unknown): void;
}

export const browserFrames: FrameScheduler = {
	request: (cb) => requestAnimationFrame(() => cb()),
	cancel: (h) => cancelAnimationFrame(h as number),
};

/** The queue's cap while the tab is hidden (working default: the ring's capacity). */
export const HIDDEN_QUEUE_CAP = 50_000;

export interface BatcherOptions<T> {
	frames: FrameScheduler;
	/** Called at most once per frame with everything queued and how many items were dropped from the queue since the last call. */
	flush: (items: T[], dropped: number) => void;
	/** Defaults to {@link HIDDEN_QUEUE_CAP}. */
	hiddenCap?: number;
	hidden?: boolean;
}

export class Batcher<T> {
	#frames: FrameScheduler;
	#flush: (items: T[], dropped: number) => void;
	#cap: number;
	#hidden: boolean;
	#queue: T[] = [];
	#pendingDrops = 0;
	#totalDrops = 0;
	#handle: unknown = null;
	#scheduled = false;

	constructor(opts: BatcherOptions<T>) {
		this.#frames = opts.frames;
		this.#flush = opts.flush;
		this.#cap = opts.hiddenCap ?? HIDDEN_QUEUE_CAP;
		this.#hidden = opts.hidden ?? false;
	}

	/** Items waiting for the next frame. */
	get queued(): number {
		return this.#queue.length;
	}

	/** Items dropped from the queue since the start (or the last `reset`). */
	get dropped(): number {
		return this.#totalDrops;
	}

	push(items: readonly T[]): void {
		if (items.length === 0) return;
		for (const item of items) this.#queue.push(item);
		if (this.#hidden) this.#capQueue();
		this.#schedule();
	}

	/**
	 * The tab's visibility changed. A frame asked for while hidden stays asked for: the
	 * browser runs it when the tab is visible again, and it reports what the cap dropped.
	 */
	setHidden(hidden: boolean): void {
		if (this.#hidden === hidden) return;
		this.#hidden = hidden;
		if (hidden) this.#capQueue();
	}

	/** Forgets the queue and the drop count without flushing (the panel's "clear"). */
	reset(): void {
		this.#queue = [];
		this.#pendingDrops = 0;
		this.#totalDrops = 0;
		if (this.#scheduled) {
			this.#frames.cancel(this.#handle);
			this.#scheduled = false;
			this.#handle = null;
		}
	}

	dispose(): void {
		this.reset();
	}

	#capQueue(): void {
		const over = this.#queue.length - this.#cap;
		if (over <= 0) return;
		this.#queue = this.#queue.slice(over);
		this.#pendingDrops += over;
		this.#totalDrops += over;
	}

	#schedule(): void {
		if (this.#scheduled || this.#queue.length === 0) return;
		this.#scheduled = true;
		this.#handle = this.#frames.request(() => this.#frame());
	}

	#frame(): void {
		this.#scheduled = false;
		this.#handle = null;
		const items = this.#queue;
		const dropped = this.#pendingDrops;
		this.#queue = [];
		this.#pendingDrops = 0;
		if (items.length === 0 && dropped === 0) return;
		this.#flush(items, dropped);
	}
}
