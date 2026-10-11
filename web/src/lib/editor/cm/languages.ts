// File name -> language (plan m2i O3, D3). Each language is its own dynamic import, so its
// chunk is requested only when a file of that kind opens; an unknown extension is plain
// text. Kotlin here is the legacy clike mode; step 5 decides whether a Lezer grammar replaces it.
import { StreamLanguage, type StreamParser } from '@codemirror/language';
import type { Extension } from '@codemirror/state';

export interface LanguageEntry {
	/** Stable name, for tests and diagnostics. */
	id: string;
	load: () => Promise<Extension>;
}

const stream = async (mode: () => Promise<StreamParser<unknown>>): Promise<Extension> => StreamLanguage.define(await mode());

const kotlin: LanguageEntry = { id: 'kotlin', load: () => stream(async () => (await import('@codemirror/legacy-modes/mode/clike')).kotlin) };
const groovy: LanguageEntry = { id: 'groovy', load: () => stream(async () => (await import('@codemirror/legacy-modes/mode/groovy')).groovy) };
const toml: LanguageEntry = { id: 'toml', load: () => stream(async () => (await import('@codemirror/legacy-modes/mode/toml')).toml) };
const properties: LanguageEntry = {
	id: 'properties',
	load: () => stream(async () => (await import('@codemirror/legacy-modes/mode/properties')).properties),
};
const java: LanguageEntry = { id: 'java', load: async () => (await import('@codemirror/lang-java')).java() };
const xml: LanguageEntry = { id: 'xml', load: async () => (await import('@codemirror/lang-xml')).xml() };
const json: LanguageEntry = { id: 'json', load: async () => (await import('@codemirror/lang-json')).json() };
const markdown: LanguageEntry = { id: 'markdown', load: async () => (await import('@codemirror/lang-markdown')).markdown() };

/** Extension (lower case, with its dot) -> language. */
export const BY_EXTENSION: Readonly<Record<string, LanguageEntry>> = {
	'.kt': kotlin,
	'.kts': kotlin,
	'.java': java,
	'.xml': xml,
	'.json': json,
	'.md': markdown,
	'.gradle': groovy,
	'.toml': toml,
	'.properties': properties,
};

/** The language of a file path or name, or null for plain text. */
export function languageFor(path: string): LanguageEntry | null {
	const name = path.slice(path.lastIndexOf('/') + 1);
	const dot = name.lastIndexOf('.');
	if (dot < 0) return null;
	return Object.hasOwn(BY_EXTENSION, name.slice(dot).toLowerCase()) ? BY_EXTENSION[name.slice(dot).toLowerCase()]! : null;
}
