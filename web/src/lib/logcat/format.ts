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

/** The line's parts, so a row can style them apart; formatLine joins them. */
export function lineParts(l: StreamLine): { time: string; pid: string; rest: string; level: string; text: string } {
	return {
		time: clockTime(l.time),
		pid: String(l.pid),
		rest: `${l.tid} ${l.package ?? '-'}`,
		level: LETTER[l.priority] ?? '?',
		text: `${l.tag}: ${l.message}`,
	};
}

export function formatLine(l: StreamLine): string {
	const p = lineParts(l);
	return `${p.time} ${p.pid} ${p.rest} ${p.level} ${p.text}`;
}

/** The class a row gets for its priority: from a fixed set, never the device's own text. */
export function priorityClass(p: Priority): string {
	return Object.hasOwn(LETTER, p) ? `p-${p}` : '';
}
