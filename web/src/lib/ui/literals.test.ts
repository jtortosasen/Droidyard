// No colour literal outside tokens.css (plan m3d step 2, ADR-0128): the palette has one home,
// so a theme change is a change to one file and the contrast floor (contrast.test.ts) covers
// every colour the UI draws. The scanner reads every .ts, .svelte and .css file under src.
import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

const NAMED = new Set(
	(
		'aliceblue antiquewhite aqua aquamarine azure beige bisque black blanchedalmond blue blueviolet brown burlywood ' +
		'cadetblue chartreuse chocolate coral cornflowerblue cornsilk crimson cyan darkblue darkcyan darkgoldenrod darkgray ' +
		'darkgreen darkgrey darkkhaki darkmagenta darkolivegreen darkorange darkorchid darkred darksalmon darkseagreen ' +
		'darkslateblue darkslategray darkslategrey darkturquoise darkviolet deeppink deepskyblue dimgray dimgrey dodgerblue ' +
		'firebrick floralwhite forestgreen fuchsia gainsboro ghostwhite gold goldenrod gray green greenyellow grey honeydew ' +
		'hotpink indianred indigo ivory khaki lavender lavenderblush lawngreen lemonchiffon lightblue lightcoral lightcyan ' +
		'lightgoldenrodyellow lightgray lightgreen lightgrey lightpink lightsalmon lightseagreen lightskyblue lightslategray ' +
		'lightslategrey lightsteelblue lightyellow lime limegreen linen magenta maroon mediumaquamarine mediumblue ' +
		'mediumorchid mediumpurple mediumseagreen mediumslateblue mediumspringgreen mediumturquoise mediumvioletred ' +
		'midnightblue mintcream mistyrose moccasin navajowhite navy oldlace olive olivedrab orange orangered orchid ' +
		'palegoldenrod palegreen paleturquoise palevioletred papayawhip peachpuff peru pink plum powderblue purple ' +
		'rebeccapurple red rosybrown royalblue saddlebrown salmon sandybrown seagreen seashell sienna silver skyblue ' +
		'slateblue slategray slategrey snow springgreen steelblue tan teal thistle tomato turquoise violet wheat white ' +
		'whitesmoke yellow yellowgreen'
	).split(' '),
);
// transparent, currentColor and inherit are not palette colours; they stay allowed.
const KEEP = new Set(['transparent', 'currentcolor', 'inherit', 'initial', 'unset', 'revert']);
const COLOUR_PROPS =
	/^(color|background|background-color|border|border-(top|right|bottom|left|block|inline)(-[a-z]+)*|outline|outline-color|fill|stroke|box-shadow|text-shadow|caret-color|accent-color|text-decoration|text-decoration-color|column-rule|--[\w-]+)$/;
const BORDER_PROPS = /^(border|border-(top|right|bottom|left|block|inline)(-(start|end))?|outline)$/;
const BORDER_STYLE = /\b(solid|dashed|dotted|double|groove|ridge|inset|outset|hidden)\b/;
const HEX = /(?<![\w&#-])#(?:[0-9a-f]{8}|[0-9a-f]{6}|[0-9a-f]{3,4})\b/i;
const FUNC = /\b(rgb|rgba|hsl|hsla|hwb|lab|lch|oklab|oklch)\(/gi;

/** Strips comments from code, keeping the string literals (a string is where a colour hides in TS). */
const TOKENS = /'(?:\\.|[^'\\\n])*'|"(?:\\.|[^"\\\n])*"|`(?:\\.|[^`\\])*`|\/\*[\s\S]*?\*\/|\/\/[^\n]*/g;

export function codeStrings(code: string): string[] {
	return (code.replace(/<!--[\s\S]*?-->/g, '').match(TOKENS) ?? []).filter((t) => !t.startsWith('/'));
}

function functions(text: string, into: string[]): void {
	for (const m of text.matchAll(FUNC)) {
		let depth = 1;
		let i = m.index + m[0].length;
		const start = i;
		for (; i < text.length && depth > 0; i++) depth += text[i] === '(' ? 1 : text[i] === ')' ? -1 : 0;
		const args = text.slice(start, i - 1);
		// A channel is literal unless the argument is var()s, calc() of them and keywords: `hsl(var(--h) var(--s) var(--l))`.
		if (/\d/.test(args.replace(/var\([^)]*\)/g, ''))) into.push(`${m[1]}(${args}) has a literal channel`);
	}
}

/** The violations of a CSS text (a .css file or the inside of a <style> block). */
export function scanCSS(css: string): string[] {
	const out: string[] = [];
	const text = css.replace(/\/\*[\s\S]*?\*\//g, '');
	for (const m of text.matchAll(/([\w-]+)\s*:\s*([^;{}]+)/g)) {
		const prop = m[1].toLowerCase();
		const value = m[2].trim();
		// A hex or a colour function is a literal in any declaration (border-color, a gradient);
		// a named colour is only read as one in a colour property, where the word cannot mean anything else.
		if (HEX.test(value)) out.push(`${prop}: ${value} has a hex colour`);
		functions(value, out);
		if (!COLOUR_PROPS.test(prop) && !/--/.test(prop)) continue;
		const words = value.replace(/var\([^)]*\)|url\([^)]*\)/g, ' ').toLowerCase().split(/[^a-z-]+/);
		for (const w of words) if (NAMED.has(w) && !KEEP.has(w)) out.push(`${prop}: ${value} has the named colour ${w}`);
		if (BORDER_PROPS.test(prop) && BORDER_STYLE.test(value)) {
			const hasColour = /var\(|#|\b(rgb|hsl)a?\(|color-mix\(/i.test(value) || words.some((w) => NAMED.has(w) || KEEP.has(w));
			if (!hasColour) out.push(`${prop}: ${value} has a style and no colour (it falls back to currentColor)`);
		}
	}
	return out;
}

/** The violations of a .ts or .svelte file: strings in code, and every <style> block as CSS. */
export function scanSource(path: string, text: string): string[] {
	const out: string[] = [];
	const styles = [...text.matchAll(/<style[^>]*>([\s\S]*?)<\/style>/g)];
	for (const s of styles) out.push(...scanCSS(s[1]));
	const rest = text.replace(/<style[^>]*>[\s\S]*?<\/style>/g, '');
	for (const s of codeStrings(rest)) {
		if (HEX.test(s)) out.push(`string ${s} has a hex colour`);
		functions(s, out);
	}
	return out.map((v) => `${path}: ${v}`);
}

const FACES = /^(var\(--font-ui\)|var\(--font-mono\)|inherit)$/;

/** The type violations of a CSS text: a size or line height that is not a token, a face that is not one of the two. */
export function scanType(css: string): string[] {
	const out: string[] = [];
	// An @font-face block declares a face; it is the one place a family name is a literal (fonts.css).
	const text = css.replace(/\/\*[\s\S]*?\*\//g, '').replace(/@font-face\s*\{[^}]*\}/g, '');
	for (const m of text.matchAll(/(?<![\w-])(font-size|line-height|font-family|font)\s*:\s*([^;{}]+)/g)) {
		const prop = m[1];
		const value = m[2].trim();
		if (/^(inherit|initial|unset|revert)$/.test(value) && prop !== 'font-family') continue;
		if (prop === 'font-family') {
			if (!FACES.test(value)) out.push(`font-family: ${value} is not var(--font-ui), var(--font-mono) or inherit`);
		} else if (prop === 'font') {
			// The shorthand carries a size, a line height and a face; whatever var() does not cover is a literal.
			if (/\d|\b(monospace|sans-serif|serif|system-ui|ui-monospace)\b|['"]/.test(value.replace(/var\([^)]*\)/g, ''))) {
				out.push(`font: ${value} has a literal`);
			}
		} else if (!/var\(/.test(value) && value !== 'normal') {
			out.push(`${prop}: ${value} is a literal, not a token`);
		}
	}
	return out;
}

/** The type violations of a .ts or .svelte file: <style> blocks, CSS in strings and camelCase style objects. */
export function scanTypeSource(path: string, text: string): string[] {
	const out: string[] = [];
	for (const s of text.matchAll(/<style[^>]*>([\s\S]*?)<\/style>/g)) out.push(...scanType(s[1]));
	const rest = text.replace(/<style[^>]*>[\s\S]*?<\/style>/g, '');
	for (const s of codeStrings(rest)) out.push(...scanType(s));
	for (const m of rest.matchAll(/\b(fontSize|lineHeight|fontFamily)\s*:\s*([^,}\n]+)/g)) {
		const v = m[2].trim().replace(/^['"`]|['"`],?$/g, '');
		const ok = m[1] === 'fontFamily' ? FACES.test(v) : /var\(/.test(v) || v === 'inherit' || v === 'normal';
		if (!ok) out.push(`${m[1]}: ${v} is a literal`);
	}
	return out.map((v) => `${path}: ${v}`);
}

function definitions(text: string, into: Set<string>): void {
	for (const m of text.matchAll(/(?<![\w-])(--[\w-]+)\s*:/g)) into.add(m[1]); // `--x:` in CSS and style="--x: …"
	for (const m of text.matchAll(/\bstyle:(--[\w-]+)/g)) into.add(m[1]); // `style:--x={…}`
	for (const m of text.matchAll(/setProperty\(\s*['"`](--[\w-]+)/g)) into.add(m[1]);
	for (const m of text.matchAll(/<[A-Z]\w*[^>]*\s(--[\w-]+)=/g)) into.add(m[1]); // a component's `--x=` prop
}

function uses(text: string): string[] {
	return [...text.replace(/\/\*[\s\S]*?\*\//g, '').matchAll(/var\(\s*(--[\w-]+)(?![\w-]|\$\{)/g)].map((m) => m[1]);
}

const TS_SVELTE = import.meta.glob<string>('/src/**/*.{ts,svelte}', { query: '?raw', import: 'default', eager: true });
const CSS_LISTED = Object.keys(import.meta.glob('/src/**/*.css'));
const read = (key: string): string => new TextDecoder().decode(readFileSync(new URL('../../..' + key, import.meta.url)));

const TOKENS_CSS = '/src/lib/ui/tokens.css';
const CSS_SCANNED = ['/src/lib/ui/base.css', '/src/lib/ui/fonts.css'];

const isTest = (key: string): boolean => /\.test\.ts$/.test(key);
const SOURCES = Object.entries(TS_SVELTE).filter(([k]) => !isTest(k));
const STYLESHEETS = CSS_SCANNED.map((k) => [k, read(k)] as const);

describe('colour literals', () => {
	it('scans every source and every stylesheet', () => {
		expect(SOURCES.length).toBeGreaterThan(50);
		for (const [k, t] of SOURCES) expect(t.length, k).toBeGreaterThan(0);
		for (const [k, t] of STYLESHEETS) expect(t.length, k).toBeGreaterThan(0);
		// The stylesheets read are all of them: a new .css file must be added to CSS_SCANNED.
		expect([...CSS_SCANNED].sort()).toEqual(CSS_LISTED.filter((k) => k !== TOKENS_CSS).sort());
		expect(read(TOKENS_CSS).length).toBeGreaterThan(0);
	});

	it('has no hex, rgb(), hsl() or named colour, and no colourless border, outside tokens.css', () => {
		const violations = [
			...SOURCES.flatMap(([k, t]) => scanSource(k, t)),
			...STYLESHEETS.flatMap(([k, t]) => scanCSS(t).map((v) => `${k}: ${v}`)),
		];
		expect(violations, violations.join('\n')).toEqual([]);
	});

	// Decision (ADR-0128): sizes, line heights and faces are tokens, so the type scale has one home
	// and the 11.5px floor (scale.test.ts) covers every size the UI draws.
	it('has no font-size, line-height or font-family literal outside tokens.css', () => {
		const violations = [
			...SOURCES.flatMap(([k, t]) => scanTypeSource(k, t)),
			...STYLESHEETS.flatMap(([k, t]) => scanType(t).map((v) => `${k}: ${v}`)),
		];
		expect(violations, violations.join('\n')).toEqual([]);
	});

	// Decision: a var() to a name nobody defines is silently the initial value (no colour at all).
	it('uses no custom property that no scanned file defines', () => {
		const defined = new Set<string>();
		const all = [...SOURCES.map(([k, t]) => [k, t] as const), ...STYLESHEETS, [TOKENS_CSS, read(TOKENS_CSS)] as const];
		for (const [, t] of all) definitions(t, defined);
		const unknown = all.flatMap(([k, t]) => uses(t).filter((n) => !defined.has(n)).map((n) => `${k}: var(${n}) is defined nowhere`));
		expect(defined.size).toBeGreaterThan(20);
		expect(unknown).toEqual([]);
	});
});

describe('the literal scanner', () => {
	const bad = [
		'color: #fff;',
		'background: #8a5a00',
		'border: 1px solid #ffda6a;',
		'background: rgb(0 0 0 / 0.4);',
		'box-shadow: 0 0 16px rgb(0 0 0 / 0.35);',
		'background: hsl(calc(var(--ri) * 137.5) 55% 50%);',
		'color: white;',
		'background: red',
		'border: 1px solid;',
		'border-left: 3px dashed',
		'border-color: #f00;',
		'background-image: linear-gradient(#000, #fff);',
		'scrollbar-color: rgb(1 2 3) transparent;',
	];
	const good = [
		'color: var(--text);',
		'background: transparent;',
		'border: 2px solid transparent;',
		'border-color: transparent;',
		'border: 1px solid var(--border);',
		'border-bottom: 0;',
		'color: currentColor;',
		'background: hsl(var(--hue) var(--avatar-s) var(--avatar-l));',
		'box-shadow: 0 0 0 1.5px var(--bg-panel), 0 0 0 2.5px var(--text-2);',
		'background: color-mix(in srgb, var(--accent-soft) calc(var(--attract) * 100%), transparent);',
		'white-space: nowrap;',
		'width: 52px;',
	];
	it.each(bad)('flags %s', (line) => {
		expect(scanCSS(`.x { ${line} }`), line).not.toEqual([]);
	});
	it.each(good)('passes %s', (line) => {
		expect(scanCSS(`.x { ${line} }`), line).toEqual([]);
	});

	it.each([
		'font-size: 12px;',
		'font-size: 0.85rem;',
		'font-size: 0.8em;',
		'line-height: 18px;',
		'line-height: 1.5;',
		'font: 12px/18px ui-monospace, monospace;',
		'font: var(--fs-base) / 1.4 var(--font-ui);',
		"font-family: 'Geist Variable', sans-serif;",
		'font-family: ui-monospace, monospace;',
	])('flags the type literal %s', (line) => {
		expect(scanType(`.x { ${line} }`), line).not.toEqual([]);
	});
	it.each([
		'font-size: var(--fs-small);',
		'font-size: inherit;',
		'line-height: var(--log-row);',
		'font: inherit;',
		'font: var(--fs-base) / var(--lh-base) var(--font-ui);',
		'font-family: var(--font-mono);',
		'font-family: inherit;',
	])('passes the type value %s', (line) => {
		expect(scanType(`.x { ${line} }`), line).toEqual([]);
	});
	it('scans camelCase style objects in TypeScript', () => {
		expect(scanTypeSource('f.ts', "const t = { fontSize: '12px' };")).not.toEqual([]);
		expect(scanTypeSource('f.ts', "const t = { lineHeight: '1.5' };")).not.toEqual([]);
		expect(scanTypeSource('f.ts', "const t = { fontSize: 'var(--fs-mono)', lineHeight: 'var(--lh-code)' };")).toEqual([]);
	});

	it('flags a hex in a quoted string and not a private field', () => {
		expect(scanSource('f.ts', `const c = '#ff0000';`)).not.toEqual([]);
		expect(scanSource('f.ts', 'const c = "1px solid #abc";')).not.toEqual([]);
		expect(scanSource('f.svelte', '<div style="color: #fff"></div>')).not.toEqual([]);
		expect(scanSource('f.svelte', '<style>\n.a { color: #fff; }\n</style>')).not.toEqual([]);
		expect(scanSource('f.ts', 'class A { #add(x: string) {} f() { this.#add("a"); this.#dead = true; } #dead = false; }')).toEqual([]);
		expect(scanSource('f.ts', "const sel = '#app'; // #fff in a comment\n")).toEqual([]);
		expect(scanSource('f.ts', 'const s = `&#123; ${x}`;')).toEqual([]);
	});

	it('finds the custom properties a file defines and uses', () => {
		const defined = new Set<string>();
		definitions('a { --x: 1 } <div style:--ri={n} style="--y: 2"> <Foo --z="1" /> el.style.setProperty("--w", "1")', defined);
		expect([...defined].sort()).toEqual(['--w', '--x', '--y', '--ri', '--z'].sort());
		expect(uses('color: var(--a); b: var( --b , red); c: var(${x})')).toEqual(['--a', '--b']);
	});
});
