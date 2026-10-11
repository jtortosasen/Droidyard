import { describe, expect, it } from 'vitest';

// Plan m2i D3 (ADR-0009 §Consequences): the editor library stays replaceable because only
// lib/editor/cm/ imports it, and the rest of the app reaches that directory by a dynamic
// import(), so its chunk is not part of the entry. This reads the sources and fails on
// either breach.

// Every source of the app as text (Vite's glob, so no Node types are needed); the keys are
// root-relative paths such as "/src/lib/editor/viewer.ts".
const ALL = import.meta.glob<string>('/src/**/*.{ts,js,svelte}', { query: '?raw', import: 'default', eager: true });
const CM = '/src/lib/editor/cm/';
const SELF = '/src/lib/editor/boundary.test.ts';

const LIBRARY = /^(@codemirror\/|@lezer\/|codemirror$|style-mod$)/;
const CM_DIR = /(^|\/)cm(\/|$)/;

/** Module specifiers of a file's static imports and re-exports (`import type` included), and its dynamic ones. */
export function specifiers(source: string): { static: string[]; dynamic: string[] } {
	const stat = [...source.matchAll(/\b(?:import|export)\s+(?:type\s+)?(?:[\w*${},\s]+?\s+from\s+)?(['"])([^'"\n]+)\1/g)].map((m) => m[2]!);
	const dyn = [...source.matchAll(/\bimport\s*\(\s*(['"])([^'"\n]+)\1\s*\)/g)].map((m) => m[2]!);
	return { static: stat, dynamic: dyn };
}

/** The ways a file outside cm/ breaks the boundary; empty when it keeps it. */
export function breaches(source: string): string[] {
	const { static: stat, dynamic } = specifiers(source);
	const out: string[] = [];
	for (const s of [...stat, ...dynamic]) if (LIBRARY.test(s)) out.push(`imports the editor library "${s}"`);
	for (const s of stat) if (CM_DIR.test(s)) out.push(`imports "${s}" statically (only import() may)`);
	return out;
}

describe('the editor seam', () => {
	// Decision (D3): a static import of the library or of cm/ outside cm/ is a breach; a
	// dynamic import of cm/ is the one allowed way in.
	it('flags what breaks the boundary and lets the dynamic import through', () => {
		expect(breaches(`import { EditorView } from '@codemirror/view';`)).toHaveLength(1);
		expect(breaches(`import type { Extension } from '@codemirror/state'`)).toHaveLength(1);
		expect(breaches(`import { tags } from "@lezer/highlight"`)).toHaveLength(1);
		expect(breaches(`export { x } from '@codemirror/language'`)).toHaveLength(1);
		expect(breaches(`const m = await import('@codemirror/view')`)).toHaveLength(1);
		expect(breaches(`import { mount } from './cm/view';`)).toHaveLength(1);
		expect(breaches(`import type { T } from '../editor/cm/theme'`)).toHaveLength(1);
		expect(breaches(`import './cm/view'`)).toHaveLength(1);
		expect(breaches(`export const load = () => import('./cm/view');`)).toEqual([]);
		expect(breaches(`import { ViewerSet } from './viewer';\nimport { x } from './cmd/view';`)).toEqual([]);
	});

	it('is kept by every source outside lib/editor/cm/', () => {
		const files = Object.entries(ALL).filter(([f]) => !f.startsWith(CM) && f !== SELF);
		// A walk that found nothing would pass for the wrong reason.
		expect(files.length).toBeGreaterThan(50);
		const found = files.flatMap(([f, src]) => breaches(src).map((b) => `${f} ${b}`));
		expect(found).toEqual([]);
	});

	it('has a cm/ directory that imports the library (the glob looks where it should)', () => {
		const using = Object.entries(ALL).filter(([f, src]) => f.startsWith(CM) && /from '@codemirror\//.test(src));
		expect(using.length).toBeGreaterThan(0);
	});
});
