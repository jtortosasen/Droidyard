import { describe, expect, it } from 'vitest';
import { unpinned } from './check-pins.mjs';

const pm = 'pnpm@12.10.1+sha512.abc';

describe('check-pins', () => {
	// Decision (D2): exact versions only. A range lets a fresh install resolve to
	// something nobody reviewed, so each range spelling is refused by name.
	it.each(['^8.3.4', '~8.3.4', '>=8.3.4', '8.x', '*', 'latest', 'workspace:*', 'github:a/b'])(
		'refuses %s',
		(spec) => {
			const problems = unpinned({ packageManager: pm, devDependencies: { vite: spec } });
			expect(problems).toHaveLength(1);
			expect(problems[0]).toContain('devDependencies.vite');
		},
	);

	it('accepts exact versions, prereleases included, in every section', () => {
		expect(
			unpinned({
				packageManager: pm,
				dependencies: { a: '1.2.3' },
				devDependencies: { b: '1.2.3-rc.1' },
				optionalDependencies: { c: '10.0.0' },
			}),
		).toEqual([]);
	});

	it('refuses an unpinned package manager', () => {
		expect(unpinned({})).toHaveLength(1);
		expect(unpinned({ packageManager: 'pnpm@^12' })).toHaveLength(1);
		expect(unpinned({ packageManager: 'npm@10.0.0' })).toHaveLength(1);
	});

	it('is satisfied by this package.json', async () => {
		const { default: pkg } = await import('../package.json', { with: { type: 'json' } });
		expect(unpinned(pkg)).toEqual([]);
	});
});
