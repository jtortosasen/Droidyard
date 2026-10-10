// The lines the console panel holds (plan m2c step 9, D18): the newest build's last
// 20,000 lines, with the byte position of the first one, so the part of the console before
// it can be read back with `artifact.read` ("Earlier", plan m2b D14). Each line has a
// `seq` that only moves with it, so a row keeps its key while older rows are forgotten
// from the front or earlier ones are added before it.

/** D18's working default: how many lines of the newest build the panel keeps. */
export const CONSOLE_LINES = 20_000;

/** The bytes of `s` in UTF-8, without building them (the daemon counts offsets in bytes). */
export function utf8Length(s: string): number {
	let n = 0;
	for (let i = 0; i < s.length; i++) {
		const c = s.charCodeAt(i);
		if (c < 0x80) n += 1;
		else if (c < 0x800) n += 2;
		else if (c >= 0xd800 && c < 0xdc00 && i + 1 < s.length && (s.charCodeAt(i + 1) & 0xfc00) === 0xdc00) {
			n += 4; // a surrogate pair is one 4-byte rune
			i++;
		} else n += 3; // a lone surrogate encodes as U+FFFD, three bytes
	}
	return n;
}

/** The lines of a read's text: each ends in a newline, so the empty piece after the last one is not a line. */
export function splitLines(text: string): string[] {
	if (text === '') return [];
	const lines = text.split('\n');
	if (lines[lines.length - 1] === '') lines.pop();
	return lines;
}

export interface Row {
	seq: number;
	text: string;
}

export class LineStore {
	readonly capacity: number;
	#lines: string[] = [];
	#startSeq = 0;
	#startOffset = 0;
	#evicted = 0;

	constructor(capacity: number = CONSOLE_LINES) {
		if (!Number.isInteger(capacity) || capacity < 1) throw new RangeError('capacity must be a positive integer');
		this.capacity = capacity;
	}

	get count(): number {
		return this.#lines.length;
	}

	/** The seq of the first line held. */
	get startSeq(): number {
		return this.#startSeq;
	}

	/** The byte position of the first line held in the console; 0 means the console's start. */
	get startOffset(): number {
		return this.#startOffset;
	}

	/** How many lines were forgotten from the front to stay under the cap, since the last `clear`. */
	get evicted(): number {
		return this.#evicted;
	}

	/** Forgets every line; the next one is at `startOffset`. Seqs keep growing. */
	clear(startOffset = 0): void {
		this.#startSeq += this.#lines.length;
		this.#lines = [];
		this.#startOffset = startOffset;
		this.#evicted = 0;
	}

	/** Adds lines at the end and forgets the oldest ones beyond the capacity. */
	append(lines: readonly string[]): void {
		for (const l of lines) this.#lines.push(l);
		const over = this.#lines.length - this.capacity;
		if (over <= 0) return;
		let bytes = 0;
		for (let i = 0; i < over; i++) bytes += utf8Length(this.#lines[i]!) + 1;
		this.#lines.splice(0, over);
		this.#startSeq += over;
		this.#startOffset += bytes;
		this.#evicted += over;
	}

	/** Adds the lines that precede the first one held; `startOffset` is where the first of them starts. */
	prepend(lines: readonly string[], startOffset: number): void {
		this.#lines = lines.concat(this.#lines);
		this.#startSeq -= lines.length;
		this.#startOffset = startOffset;
	}

	rows(start: number, end: number): Row[] {
		const out: Row[] = [];
		for (let i = Math.max(0, start); i < Math.min(end, this.#lines.length); i++) {
			out.push({ seq: this.#startSeq + i, text: this.#lines[i]! });
		}
		return out;
	}
}
