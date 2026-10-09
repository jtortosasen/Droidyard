// The panel's line store (plan m2a D8: 50,000 entries): a circular buffer that keeps the
// newest `capacity` items and counts what it had to forget. Items have an absolute index
// that only grows, so a view can name an item in a way that survives eviction.

export const RING_CAPACITY = 50_000;

export class Ring<T> {
	readonly capacity: number;
	#buf: (T | undefined)[];
	#end = 0; // the absolute index one past the newest item
	#length = 0;
	#evicted = 0;

	constructor(capacity: number) {
		if (!Number.isInteger(capacity) || capacity < 1) throw new RangeError('capacity must be a positive integer');
		this.capacity = capacity;
		this.#buf = new Array<T | undefined>(capacity);
	}

	/** How many items are held. */
	get length(): number {
		return this.#length;
	}

	/** The absolute index of the oldest held item (equal to `end` when empty). */
	get first(): number {
		return this.#end - this.#length;
	}

	/** The absolute index one past the newest item: the index the next push gets. */
	get end(): number {
		return this.#end;
	}

	/** How many items were forgotten to make room since the last `clear`. Never silent. */
	get evicted(): number {
		return this.#evicted;
	}

	/** Appends `item`; returns true when the oldest item was forgotten to make room. */
	push(item: T): boolean {
		const slot = this.#end % this.capacity;
		const full = this.#length === this.capacity;
		this.#buf[slot] = item;
		this.#end++;
		if (full) {
			this.#evicted++;
			return true;
		}
		this.#length++;
		return false;
	}

	/** The item at an absolute index, undefined when it was forgotten or not yet pushed. */
	at(index: number): T | undefined {
		if (index < this.first || index >= this.#end) return undefined;
		return this.#buf[index % this.capacity];
	}

	/** Forgets everything held and the eviction count; absolute indexes keep growing. */
	clear(): void {
		this.#buf = new Array<T | undefined>(this.capacity);
		this.#length = 0;
		this.#evicted = 0;
	}
}
