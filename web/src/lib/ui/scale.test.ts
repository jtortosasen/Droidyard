// The type scale (plan m3d step 4, ADR-0128): read from tokens.css itself, as contrast.test.ts
// does. The owner's complaint was that the type is far too small, so the floor is a test:
// no size token below 11.5px, and the scale keeps its order.
import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import { LOG_ROW_PX } from './metrics';

const css = new TextDecoder().decode(readFileSync(new URL('./tokens.css', import.meta.url))).replace(/\/\*[\s\S]*?\*\//g, '');

function sizes(): Map<string, number> {
	const out = new Map<string, number>();
	for (const m of css.matchAll(/(--fs-[\w-]+)\s*:\s*([\d.]+)px\s*;/g)) out.set(m[1], parseFloat(m[2]));
	return out;
}

describe('the type scale', () => {
	const fs = sizes();

	// Decision: no visible text under 11.5px. 11px small text is what the facelift replaces.
	it('has every --fs-* at 11.5px or more', () => {
		const small = [...fs].filter(([, px]) => px < 11.5).map(([n, px]) => `${n} is ${px}px`);
		expect(small, small.join('\n')).toEqual([]);
		expect(fs.size).toBeGreaterThanOrEqual(8);
	});

	it('names the scale the plan fixes', () => {
		expect(Object.fromEntries(fs)).toMatchObject({
			'--fs-label': 11.5,
			'--fs-small': 12,
			'--fs-base': 13.5,
			'--fs-title': 14.5,
			'--fs-large': 16,
			'--fs-display': 18,
			'--fs-mono': 13.5,
			'--fs-log': 12.5,
		});
	});

	it('orders the UI scale from label to display', () => {
		const o = ['--fs-label', '--fs-small', '--fs-base', '--fs-title', '--fs-large', '--fs-display'].map((n) => fs.get(n) ?? 0);
		expect(o).toEqual([...o].sort((a, b) => a - b));
	});

	// Decision: the log row has one source in script (LOG_ROW_PX) and the token is its twin, so
	// the virtualised lists' window arithmetic and the drawn row cannot drift apart.
	it('keeps --lh-log equal to LOG_ROW_PX', () => {
		expect(css.match(/--lh-log\s*:\s*(\d+)px/)?.[1]).toBe(String(LOG_ROW_PX));
	});

	// Decision: both densities define the same row and tab tokens, comfortable taller than compact.
	it('gives each density a row, a tab and a file-tab height, comfortable taller', () => {
		for (const name of ['--row', '--tab-height', '--ftab-height']) {
			const re = new RegExp(`\\[data-density='(compact|comfortable)'\\]\\s*\\{[^}]*${name}\\s*:\\s*(\\d+)px`, 'g');
			const v = Object.fromEntries([...css.matchAll(re)].map((m) => [m[1], parseInt(m[2], 10)]));
			expect(v.comfortable, name).toBeGreaterThan(v.compact);
		}
	});
});
