// Fails when a dependency is not pinned to an exact version (plan m2a D2, ADR-0039 §3).
// A range (^, ~, >=, *, a tag, a URL) would let a fresh install differ from the
// lockfile's review, so every spec in package.json must be exactly one version.
import { readFileSync } from 'node:fs';
import { pathToFileURL } from 'node:url';

const exact = /^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$/;
const sections = ['dependencies', 'devDependencies', 'optionalDependencies'];

/** The problems in a parsed package.json: one string per spec that is not exact. */
export function unpinned(pkg) {
	const problems = [];
	for (const section of sections) {
		for (const [name, spec] of Object.entries(pkg[section] ?? {})) {
			if (typeof spec !== 'string' || !exact.test(spec)) {
				problems.push(`${section}.${name}: "${spec}" is not an exact version`);
			}
		}
	}
	if (!/^pnpm@\d+\.\d+\.\d+/.test(pkg.packageManager ?? '')) {
		problems.push(`packageManager: "${pkg.packageManager}" does not pin pnpm@<exact version>`);
	}
	return problems;
}

if (import.meta.url === pathToFileURL(process.argv[1] ?? '').href) {
	const pkg = JSON.parse(readFileSync(new URL('../package.json', import.meta.url), 'utf8'));
	const problems = unpinned(pkg);
	for (const p of problems) console.error(`check-pins: ${p}`);
	if (problems.length > 0) process.exit(1);
	console.log('check-pins: every dependency is pinned to an exact version');
}
