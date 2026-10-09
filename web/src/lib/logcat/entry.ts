// What the Logcat panel holds: a device line, or a marker the panel itself adds where
// something was lost or the stream ended (plan m2a step 5). Both are plain data; the
// panel renders every field as text.
import type { EventPayload } from '../api/capabilities.gen';

/** One line as device.watchLogs delivers it. `tag` and `message` are device text. */
export type StreamLine = EventPayload<'device.watchLogs', 'lines'>['lines'][number];
export type Priority = StreamLine['priority'];

/** The priorities from least to most severe: the order the priority filter uses. */
export const PRIORITIES: readonly Priority[] = ['verbose', 'debug', 'info', 'warn', 'error', 'fatal'];

export type MarkerKind =
	/** The daemon reported lines it did not deliver (`dropped` of a lines event). */
	| 'dropped'
	/** The bus reported events it did not deliver (`events.gap`). */
	| 'gap'
	/** The stream ended (`stopped`). */
	| 'stopped'
	/** The tab was hidden and the queue overflowed. */
	| 'hidden';

export interface Marker {
	marker: MarkerKind;
	text: string;
}

export type Entry = StreamLine | Marker;

/** A line never has a `marker` field, so this tells the two apart without a wrapper. */
export function isMarker(e: Entry): e is Marker {
	return 'marker' in e;
}
