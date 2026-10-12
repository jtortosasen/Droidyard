import { describe, expect, it } from 'vitest';
import { languageFor } from './languages';

const id = (p: string) => languageFor(p)?.id ?? null;

describe('languageFor', () => {
	// Decision (plan m2i step 4): the mapping table of O3; .kts is Kotlin, not plain text.
	it('maps each extension to its language', () => {
		expect(id('app/src/main/java/App.kt')).toBe('kotlin');
		expect(id('build.gradle.kts')).toBe('kotlin');
		expect(id('Foo.java')).toBe('java');
		expect(id('app/src/main/AndroidManifest.xml')).toBe('xml');
		expect(id('package.json')).toBe('json');
		expect(id('README.md')).toBe('markdown');
		expect(id('app/build.gradle')).toBe('groovy');
		expect(id('gradle/libs.versions.toml')).toBe('toml');
		expect(id('gradle.properties')).toBe('properties');
	});

	// Decision: anything else is plain text, and only the file name's extension counts.
	it('gives plain text (null) for the rest, and ignores directory dots', () => {
		expect(id('Makefile')).toBeNull();
		expect(id('notes.txt')).toBeNull();
		expect(id('dir.kt/README')).toBeNull();
		expect(id('')).toBeNull();
		expect(id('constructor')).toBeNull();
		expect(id('a.constructor')).toBeNull();
	});

	// Decision: the extension is matched without regard to case.
	it('is case-insensitive', () => {
		expect(id('Main.KT')).toBe('kotlin');
	});
});

describe('braceFold', () => {
	// Decision (m2i step 6, Delegated): Kotlin and Groovy fold at braces; the legacy modes give no folding.
	it('folds a Kotlin block from its { to its }, and not a one-line block', async () => {
		const { foldable } = await import('@codemirror/language');
		const { EditorState } = await import('@codemirror/state');
		const text = 'class A {\n    fun f() { }\n    val s = "{"\n}\n';
		const state = EditorState.create({ doc: text, extensions: [await languageFor('A.kt')!.load()] });
		const first = state.doc.line(1);
		expect(foldable(state, first.from, first.to)).toEqual({ from: 9, to: text.lastIndexOf('}') });
		const second = state.doc.line(2);
		expect(foldable(state, second.from, second.to)).toBeNull();
		const md = EditorState.create({ doc: 'a {\n}\n', extensions: [await languageFor('a.toml')!.load()] });
		expect(foldable(md, 0, 3)).toBeNull();
	});
});
