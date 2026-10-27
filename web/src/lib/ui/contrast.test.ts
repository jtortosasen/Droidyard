// The contrast floor (plan m3d step 2, ADR-0128): every text-on-surface pair and every mark
// the UI draws, in both themes, read from tokens.css itself. A palette that fails here does
// not ship; a failure names the theme, the pair and the ratio.
import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

interface RGBA {
	r: number;
	g: number;
	b: number;
	a: number;
}

type Theme = Map<string, string>;

function stripComments(css: string): string {
	return css.replace(/\/\*[\s\S]*?\*\//g, '');
}

function declarations(body: string, into: Theme): void {
	for (const d of body.split(';')) {
		const i = d.indexOf(':');
		if (i < 0) continue;
		const name = d.slice(0, i).trim();
		if (name.startsWith('--')) into.set(name, d.slice(i + 1).trim());
	}
}

/** The body of the block that opens at `open` (the index of its `{`), and the index after its `}`. */
function block(css: string, open: number): { body: string; end: number } {
	let depth = 0;
	for (let i = open; i < css.length; i++) {
		if (css[i] === '{') depth++;
		else if (css[i] === '}' && --depth === 0) return { body: css.slice(open + 1, i), end: i + 1 };
	}
	throw new Error('tokens.css: unbalanced braces');
}

/**
 * Dark is the top-level `:root`; light is the `:root` inside `@media (prefers-color-scheme:
 * light)`, over dark. `lightOnly` is what the light block declares itself.
 */
export function parseThemes(source: string): { dark: Theme; light: Theme; lightOnly: Theme } {
	const css = stripComments(source);
	const dark: Theme = new Map();
	const lightOnly: Theme = new Map();
	let pos = 0;
	for (;;) {
		const open = css.indexOf('{', pos);
		if (open < 0) break;
		const selector = css.slice(pos, open).trim();
		const { body, end } = block(css, open);
		if (selector === ':root') declarations(body, dark);
		else if (/^@media\s*\(\s*prefers-color-scheme\s*:\s*light\s*\)$/.test(selector)) {
			const inner = body.indexOf('{');
			if (inner >= 0 && body.slice(0, inner).trim() === ':root') declarations(block(body, inner).body, lightOnly);
		}
		pos = end;
	}
	const light: Theme = new Map(dark);
	for (const [k, v] of lightOnly) light.set(k, v);
	return { dark, light, lightOnly };
}

/** Resolves `var(--x)` between tokens. */
function raw(theme: Theme, name: string, seen: string[] = []): string {
	const v = theme.get(name);
	if (v === undefined) throw new Error(`${name} is not defined`);
	const m = /^var\(\s*(--[\w-]+)\s*\)$/.exec(v);
	if (!m) return v;
	if (seen.includes(name)) throw new Error(`${name}: var() cycle`);
	return raw(theme, m[1], [...seen, name]);
}

/** `#rgb`, `#rrggbb` and `rgb(r g b / a)` only; anything else is a failure naming the token. */
export function parseColor(token: string, v: string): RGBA {
	let m = /^#([0-9a-f]{3})$/i.exec(v);
	if (m) {
		const [r, g, b] = [...m[1]].map((c) => parseInt(c + c, 16));
		return { r, g, b, a: 1 };
	}
	m = /^#([0-9a-f]{6})$/i.exec(v);
	if (m) {
		const n = parseInt(m[1], 16);
		return { r: (n >> 16) & 255, g: (n >> 8) & 255, b: n & 255, a: 1 };
	}
	m = /^rgb\(\s*(\d+(?:\.\d+)?)\s+(\d+(?:\.\d+)?)\s+(\d+(?:\.\d+)?)\s*\/\s*(\d+(?:\.\d+)?%?)\s*\)$/.exec(v);
	if (m) {
		const a = m[4].endsWith('%') ? parseFloat(m[4]) / 100 : parseFloat(m[4]);
		return { r: +m[1], g: +m[2], b: +m[3], a };
	}
	throw new Error(`${token}: unsupported colour '${v}' (only #rgb, #rrggbb and rgb(r g b / a))`);
}

function colour(theme: Theme, name: string): RGBA {
	return parseColor(name, raw(theme, name));
}

function over(top: RGBA, base: RGBA): RGBA {
	const a = top.a + base.a * (1 - top.a);
	const c = (t: number, b: number): number => (t * top.a + b * base.a * (1 - top.a)) / (a || 1);
	return { r: c(top.r, base.r), g: c(top.g, base.g), b: c(top.b, base.b), a };
}

function luminance({ r, g, b }: RGBA): number {
	const lin = (c: number): number => {
		const s = c / 255;
		return s <= 0.04045 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
	};
	return 0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(b);
}

/** WCAG 2.2 contrast of `fg` (composited over `bg` if translucent) on an opaque `bg`. */
export function ratio(fg: RGBA, bg: RGBA): number {
	const f = over(fg, bg);
	const [hi, lo] = [luminance(f), luminance(bg)].sort((x, y) => y - x);
	return (hi + 0.05) / (lo + 0.05);
}

function hsl(h: number, s: number, l: number): RGBA {
	const k = (n: number): number => (n + h / 30) % 12;
	const a = s * Math.min(l, 1 - l);
	const f = (n: number): number => 255 * (l - a * Math.max(-1, Math.min(k(n) - 3, Math.min(9 - k(n), 1))));
	return { r: f(0), g: f(8), b: f(4), a: 1 };
}

function percent(theme: Theme, name: string): number {
	const v = raw(theme, name);
	const m = /^(\d+(?:\.\d+)?)%$/.exec(v);
	if (!m) throw new Error(`${name}: expected a percentage, got '${v}'`);
	return parseFloat(m[1]) / 100;
}

const source = new TextDecoder().decode(readFileSync(new URL('./tokens.css', import.meta.url)));
const themes = parseThemes(source);

const ROLES = ['--text', '--text-2', '--text-3', '--accent', '--success', '--warning', '--danger'];
const SURFACES = ['--bg-window', '--bg-panel', '--bg-raised', '--bg-hover'];
const SOFTS = ['--warning-soft', '--danger-soft'];
// A client's tag (ADR-0134 §6): its hue as text on its own soft background, over every surface
// a row can sit on, and as a mark.
const CLIENTS = ['claude', 'cursor', 'agent', 'other'].map((c) => `--client-${c}`);
const SYN = [...themes.dark.keys()].filter((k) => k.startsWith('--syn-'));
const LANES = [0, 1, 2, 3, 4, 5, 6, 7].map((i) => `--lane-${i}`);

/** A background: a token, a translucent token over another, or a token at a literal alpha. */
interface Bg {
	label: string;
	of: (t: Theme) => RGBA;
}
const tok = (name: string): Bg => ({ label: name, of: (t) => colour(t, name) });
const soft = (name: string, base: string): Bg => ({
	label: `${name} over ${base}`,
	of: (t) => over(colour(t, name), colour(t, base)),
});
// Review's tint of a diff row: the colour at 12 % over the window (cm/merge.ts `mix`).
const tint = (name: string): Bg => ({
	label: `${name} at 12% over --bg-window`,
	of: (t) => over({ ...colour(t, name), a: 0.12 }, colour(t, '--bg-window')),
});

function pairs(): Array<{ fg: string; bg: Bg; min: number }> {
	const out: Array<{ fg: string; bg: Bg; min: number }> = [];
	const add = (fgs: string[], bgs: Bg[], min: number): void => {
		for (const fg of fgs) for (const bg of bgs) out.push({ fg, bg, min });
	};
	// Text roles on every surface they sit on, and on the tinted ones.
	// The selection tint sits over every surface a list lives on (the device tree on the window,
	// the palette and toolbar chips on raised); badges sit on --bg-active.
	const BASES = ['--bg-window', '--bg-panel', '--bg-raised'];
	add(ROLES, [...SURFACES.map(tok), tok('--bg-active'), ...BASES.map((b) => soft('--accent-soft', b))], 4.5);
	add(ROLES, SOFTS.flatMap((s) => BASES.map((b) => soft(s, b))), 4.5);
	// The quiet surface of a chat not connected to Droidyard: every text role is read on it.
	add(ROLES, [tok('--bg-quiet')], 4.5);
	for (const c of CLIENTS) {
		add([c], [...BASES, '--bg-hover', '--bg-quiet'].map((b) => soft(`${c}-soft`, b)), 4.5);
		// The receded tag of a row not live: the hue over the quiet surface itself.
		add([c], [tok('--bg-quiet')], 4.5);
	}
	add(['--accent-text'], [tok('--accent')], 4.5);
	add(['--danger-text'], [tok('--danger')], 4.5);
	add(SYN, [tok('--bg-window'), tok('--bg-panel'), tok('--bg-raised'), tint('--success'), tint('--danger')], 4.5);
	// Marks (dots, lanes, the faint role): the 3:1 of WCAG 1.4.11.
	add(['--accent', '--success', '--warning', '--danger', '--text-3', ...LANES, ...CLIENTS], [tok('--bg-window'), tok('--bg-panel'), tok('--bg-raised'), tok('--bg-quiet')], 3);
	return out;
}

describe.each([
	['dark', themes.dark],
	['light', themes.light],
] as const)('contrast floor, %s', (name, theme) => {
	it('reads a non-empty tokens.css', () => {
		expect(source.length).toBeGreaterThan(0);
		expect(theme.size).toBeGreaterThan(0);
		expect(SYN.length).toBeGreaterThan(0);
	});

	it('meets WCAG 2.2 on every pair: 4.5 for text, 3 for marks', () => {
		const failures: string[] = [];
		for (const { fg, bg, min } of pairs()) {
			try {
				const r = ratio(colour(theme, fg), bg.of(theme));
				if (r < min) failures.push(`${name}: ${fg} on ${bg.label} = ${r.toFixed(2)} (needs ${min})`);
			} catch (e) {
				failures.push(`${name}: ${fg} on ${bg.label}: ${(e as Error).message}`);
			}
		}
		expect(failures, failures.join('\n')).toEqual([]);
	});

	it('meets 4.5 for the avatar initials on every hue', () => {
		const failures: string[] = [];
		try {
			const s = percent(theme, '--avatar-s');
			const l = percent(theme, '--avatar-l');
			const fg = colour(theme, '--avatar-text');
			let worst = { h: 0, r: Infinity };
			for (let h = 0; h < 360; h++) {
				const r = ratio(fg, hsl(h, s, l));
				if (r < worst.r) worst = { h, r };
			}
			if (worst.r < 4.5) failures.push(`${name}: --avatar-text on hsl(${worst.h}, --avatar-s, --avatar-l) = ${worst.r.toFixed(2)} (needs 4.5)`);
		} catch (e) {
			failures.push(`${name}: avatar: ${(e as Error).message}`);
		}
		expect(failures, failures.join('\n')).toEqual([]);
	});
});

describe('contrast reader', () => {
	// Decision: light overrides every colour dark defines, so no light surface inherits a dark
	// value by accident (a missing override would pass or fail the floor for the wrong reason).
	it('light overrides every colour token dark defines, lanes included', () => {
		expect(themes.lightOnly.size).toBeGreaterThan(0);
		const missing = [...themes.dark].filter(([k, v]) => /^(#|rgb\()/.test(v) && !themes.lightOnly.has(k)).map(([k]) => k);
		expect(missing).toEqual([]);
	});

	it('reads the two themes out of the blocks', () => {
		const t = parseThemes(':root { --a: #111; --b: var(--a); } @media (prefers-color-scheme: light) { :root { --a: #eee; } } [data-x] { --a: #f00; }');
		expect(raw(t.dark, '--b')).toBe('#111');
		expect(raw(t.light, '--b')).toBe('#eee'); // var() resolves inside the theme
		expect(t.lightOnly.has('--b')).toBe(false);
	});

	it('refuses a colour syntax it cannot evaluate, naming the token', () => {
		expect(() => parseColor('--x', 'hsl(0 0% 0%)')).toThrow(/--x/);
		expect(() => parseColor('--x', 'rgba(0, 0, 0, 0.5)')).toThrow(/--x/);
		expect(() => parseColor('--x', 'red')).toThrow(/--x/);
	});

	// Decision: WCAG 2.2 luminance, alpha composited over the surface under test.
	it('computes the WCAG ratio, with alpha composited over the surface', () => {
		const black = parseColor('k', '#000');
		const white = parseColor('w', '#ffffff');
		expect(ratio(black, white)).toBeCloseTo(21, 5);
		expect(ratio(white, white)).toBeCloseTo(1, 5);
		expect(ratio(parseColor('h', 'rgb(0 0 0 / 0.5)'), white)).toBeCloseTo(ratio(parseColor('g', '#808080'), white), 1);
	});
});
