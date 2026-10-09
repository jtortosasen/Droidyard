// The rows the panel shows: the ring's entries that pass the filter, in order. Row `i` is
// the entry at ring index `indexes[head + i]`; `seq` numbers the matches since the last
// rebuild, so a row keeps its number while older rows are evicted from the front.
import type { Entry } from './entry';
import type { Ring } from './ring';

export class View {
	#ring: Ring<Entry>;
	#match: (e: Entry) => boolean;
	#indexes: number[] = [];
	#head = 0;
	#scanned: number; // ring indexes below this have been tested
	#matched = 0; // matches since the last rebuild, including those evicted

	constructor(ring: Ring<Entry>, match: (e: Entry) => boolean) {
		this.#ring = ring;
		this.#match = match;
		this.#scanned = ring.first;
		this.#rebuild();
	}

	/** Rows held. */
	get length(): number {
		return this.#indexes.length - this.#head;
	}

	/** The seq of row 0. */
	get headSeq(): number {
		return this.#matched - this.length;
	}

	/** One past the seq of the last row. */
	get endSeq(): number {
		return this.#matched;
	}

	/** The entry of the row with this seq, undefined when it is outside the view. */
	entry(seq: number): Entry | undefined {
		const i = seq - this.headSeq;
		if (i < 0 || i >= this.length) return undefined;
		return this.#ring.at(this.#indexes[this.#head + i]!);
	}

	/** Takes in what the ring gained since the last call and drops the rows it evicted. */
	sync(): void {
		if (this.#scanned < this.#ring.first) this.#scanned = this.#ring.first; // forgotten unseen
		for (; this.#scanned < this.#ring.end; this.#scanned++) {
			const e = this.#ring.at(this.#scanned);
			if (e !== undefined && this.#match(e)) {
				this.#indexes.push(this.#scanned);
				this.#matched++;
			}
		}
		const first = this.#ring.first;
		while (this.#head < this.#indexes.length && this.#indexes[this.#head]! < first) this.#head++;
		// Reclaim the front once half of the array is spent.
		if (this.#head >= 1024 && this.#head * 2 >= this.#indexes.length) {
			this.#indexes = this.#indexes.slice(this.#head);
			this.#head = 0;
		}
	}

	/** Tests the whole ring again against a new filter; seqs start over. */
	setFilter(match: (e: Entry) => boolean): void {
		this.#match = match;
		this.#rebuild();
	}

	/** Forgets every row (the ring was cleared); seqs keep growing. */
	reset(): void {
		this.#indexes = [];
		this.#head = 0;
		this.#scanned = this.#ring.end;
	}

	#rebuild(): void {
		this.#indexes = [];
		this.#head = 0;
		this.#matched = 0;
		this.#scanned = this.#ring.first;
		this.sync();
	}
}
