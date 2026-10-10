import { describe, expect, it } from 'vitest';
import { CrashMarker } from './crash';
import type { Line } from './entry';
import { compileQuery } from './query';
import { line } from './testing';

function run(lines: Line[]): boolean[] {
	const m = new CrashMarker();
	for (const l of lines) m.mark(l);
	return lines.map((l) => l.crash === true);
}

const rt = (msg: string, over: Partial<Line> = {}) =>
	line(1, msg, { tag: 'AndroidRuntime', priority: 'error', pid: 7, ...over });

describe('crash marks (D41)', () => {
	// Decision: a Java crash is the pid's AndroidRuntime run from "FATAL EXCEPTION" to its
	// first other tag; another pid interleaved is not marked.
	it('marks only the crashing pid AndroidRuntime run', () => {
		const lines = [
			rt('about to run'), // before the run
			rt('FATAL EXCEPTION: main'),
			line(2, 'noise', { pid: 9, tag: 'AndroidRuntime' }), // another pid, same tag
			line(3, 'noise', { pid: 9, tag: 'Other' }),
			rt('Process: dev.x, PID: 7'),
			rt('java.lang.Boom', { priority: 'warn' }),
			rt('\tat dev.x.A.b(A.kt:3)'),
			line(4, 'after', { pid: 7, tag: 'Other' }), // the run ends
			rt('FATAL-less AndroidRuntime line again'), // not in a run any more
		];
		expect(run(lines)).toEqual([false, true, false, false, true, true, true, false, false]);
	});

	// Decision: the start needs an error-priority "FATAL EXCEPTION" on AndroidRuntime.
	it('does not start on other tags or priorities', () => {
		expect(run([line(1, 'FATAL EXCEPTION: main', { tag: 'Other', priority: 'error' })])).toEqual([false]);
		expect(run([rt('FATAL EXCEPTION: main', { priority: 'info' }), rt('next')])).toEqual([false, false]);
	});

	// Decision: a native crash is a fatal line tagged DEBUG or libc.
	it('marks fatal DEBUG and libc lines', () => {
		const lines = [
			line(1, 'Fatal signal 11', { tag: 'libc', priority: 'fatal' }),
			line(2, '*** ***', { tag: 'DEBUG', priority: 'fatal' }),
			line(3, 'backtrace', { tag: 'DEBUG', priority: 'info' }),
			line(4, 'x', { tag: 'Other', priority: 'fatal' }),
		];
		expect(run(lines)).toEqual([true, true, false, false]);
	});

	// Decision: markers are left alone, and a reset forgets an open run.
	it('skips markers and forgets runs on reset', () => {
		const m = new CrashMarker();
		m.mark({ marker: 'gap', text: 'x' });
		m.mark(rt('FATAL EXCEPTION: main'));
		m.reset();
		const l = rt('after reset');
		m.mark(l);
		expect(l.crash).toBeUndefined();
	});

	// Decision: the marks are what is:crash tests, and stack frames are recognised per line.
	it('feeds is:crash', () => {
		const lines = [rt('FATAL EXCEPTION: main'), rt('x'), line(2, 'ok', { pid: 7 })];
		run(lines);
		expect(lines.map((l) => compileQuery('is:crash')(l))).toEqual([true, true, false]);
	});
});
