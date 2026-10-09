// A line as one row of text. Every field is device text and stays text: the result is
// rendered as a text node, never as markup (plan m2a D5).
import type { Priority, StreamLine } from './entry';

const LETTER: Record<Priority, string> = {
	verbose: 'V',
	debug: 'D',
	info: 'I',
	warn: 'W',
	error: 'E',
	fatal: 'F',
};

/** HH:MM:SS.mmm of a device-clock RFC 3339 time ("2026-10-09T12:00:00.123456Z"). */
export function clockTime(time: string): string {
	return time.length >= 23 && time[10] === 'T' ? time.slice(11, 23) : time;
}

export function formatLine(l: StreamLine): string {
	return `${clockTime(l.time)} ${l.pid} ${l.tid} ${LETTER[l.priority] ?? '?'} ${l.tag}: ${l.message}`;
}

/** The class a row gets for its priority: from a fixed set, never the device's own text. */
export function priorityClass(p: Priority): string {
	return LETTER[p] === undefined ? '' : `p-${p}`;
}
