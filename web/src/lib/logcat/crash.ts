// Crash marks (plan m2e D41): `is:crash` needs to know which lines belong to a crash, and
// that depends on the lines around them, so each line is marked once as it arrives.
//
// A Java crash starts at an `AndroidRuntime` error line beginning "FATAL EXCEPTION" and
// takes that pid's following `AndroidRuntime` lines; lines of other pids may interleave
// and are not touched. The run ends at that pid's first line with another tag. A native
// crash is a `fatal` line tagged `DEBUG` or `libc`.
//
// Limit: a run whose process dies without another line stays open, so a new process that
// reuses the pid and logs under `AndroidRuntime` first would be marked until its next tag.
import type { Entry } from './entry';
import { isMarker } from './entry';

export class CrashMarker {
	#open = new Set<number>(); // pids inside a Java crash run

	/** Marks `e` in place when it is part of a crash. Markers are left alone. */
	mark(e: Entry): void {
		if (isMarker(e)) return;
		if (e.tag === 'AndroidRuntime') {
			if (e.priority === 'error' && e.message.startsWith('FATAL EXCEPTION')) this.#open.add(e.pid);
			if (this.#open.has(e.pid)) e.crash = true;
			return;
		}
		this.#open.delete(e.pid);
		if (e.priority === 'fatal' && (e.tag === 'DEBUG' || e.tag === 'libc')) e.crash = true;
	}

	/** Forgets the open runs (a new stream). */
	reset(): void {
		this.#open.clear();
	}
}
