import { ensureSyntaxTree } from '@codemirror/language';
import { EditorState } from '@codemirror/state';
import { highlightTree } from '@lezer/highlight';
import { tags as t, type Tag } from '@lezer/highlight';
import { describe, expect, it } from 'vitest';
import { highlightStyle } from './highlight';
import { languageFor } from './languages';

/** The highlighted spans of `text` as the language of `path` and the real style give them. */
async function spans(path: string, text: string): Promise<{ text: string; cls: string }[]> {
	const ext = await languageFor(path)!.load();
	const state = EditorState.create({ doc: text, extensions: [ext] });
	const tree = ensureSyntaxTree(state, state.doc.length, 5000);
	expect(tree).not.toBeNull();
	const out: { text: string; cls: string }[] = [];
	highlightTree(tree!, highlightStyle, (from, to, cls) => out.push({ text: text.slice(from, to), cls }));
	return out;
}

/** The class the style gives a token with these tags. */
function cls(...tags: Tag[]): string {
	const c = highlightStyle.style(tags);
	expect(c).toBeTruthy();
	return c!;
}

/** Fails unless some span of exactly `word` has the class of `tags`. */
function expectSpan(all: { text: string; cls: string }[], word: string, ...tags: Tag[]) {
	const want = cls(...tags);
	const hit = all.filter((s) => s.text === word);
	expect(hit.length, `no span "${word}" in ${JSON.stringify(all)}`).toBeGreaterThan(0);
	expect(
		hit.some((s) => s.cls.split(' ').includes(want)),
		`"${word}" has ${hit.map((h) => h.cls).join('|')}, wanted ${want}`,
	).toBe(true);
}

describe('highlighting per language (D12: the style over each language\'s tags)', () => {
	// Decision: each language, loaded as the app loads it, yields the tag the colour hangs on.
	it('Kotlin: keyword, string, comment', async () => {
		const s = await spans('A.kt', 'package a\n// note\nfun main() {\n  val x = "hi"\n}\n');
		expectSpan(s, 'fun', t.keyword);
		expectSpan(s, '"hi"', t.string);
		expectSpan(s, '// note', t.comment);
	});

	it('Kotlin script (.kts) is highlighted as Kotlin', async () => {
		const s = await spans('build.gradle.kts', 'plugins { id("x") }\nval a = 1\n');
		expectSpan(s, 'val', t.keyword);
	});

	it('Java: keyword', async () => {
		const s = await spans('A.java', 'public class A { int x = 1; }\n');
		expectSpan(s, 'class', t.keyword);
	});

	it('XML: tag and attribute', async () => {
		const s = await spans('AndroidManifest.xml', '<manifest package="a.b"><application/></manifest>\n');
		expectSpan(s, 'manifest', t.tagName);
		expectSpan(s, 'package', t.attributeName);
		expectSpan(s, '"a.b"', t.attributeValue);
	});

	it('JSON: property name', async () => {
		const s = await spans('a.json', '{"name": "x", "n": 1}\n');
		expectSpan(s, '"name"', t.propertyName);
	});

	it('Markdown: heading', async () => {
		const s = await spans('R.md', '# Title\n\ntext\n');
		expectSpan(s, ' Title', t.heading1);
	});

	it('Groovy: keyword', async () => {
		const s = await spans('app/build.gradle', 'def x = 1\nif (x) { }\n');
		expectSpan(s, 'def', t.keyword);
	});

	it('TOML: table header', async () => {
		const s = await spans('libs.toml', '[versions]\nagp = "8.0"\n');
		// The legacy TOML mode types a table header as "atom".
		expectSpan(s, '[versions]', t.atom);
		expectSpan(s, 'agp', t.propertyName);
	});

	it('properties: key', async () => {
		const s = await spans('gradle.properties', 'org.gradle.jvmargs=-Xmx2g\n');
		// The legacy properties mode types a key as "def".
		expectSpan(s, 'org.gradle.jvmargs', t.definition(t.variableName));
	});
});
