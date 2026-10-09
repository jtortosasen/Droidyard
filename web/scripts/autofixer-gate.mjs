// The svelte-autofixer gate (ADR-0039 §3). Usage, from anywhere:
//   node web/scripts/autofixer-gate.mjs <file.svelte | file.svelte.ts | file.svelte.js>...
// It calls the pinned @sveltejs/mcp package's `svelteAutofixer` on each file's contents
// and exits 1 if any file is missing or has an entry in `issues` (the CLI of the same
// package always exits 0, so it cannot be a gate). `suggestions` are printed, not fatal.
import { readFileSync } from 'node:fs';
import { basename } from 'node:path';
import { pathToFileURL } from 'node:url';
import { svelteAutofixer } from '@sveltejs/mcp';

/**
 * Runs the autofixer on one file. Returns { issues, suggestions }; a file that cannot
 * be read is reported as an issue, so a path that does not exist fails the gate.
 */
export async function inspectFile(path, autofixer = svelteAutofixer) {
	let code;
	try {
		code = readFileSync(path, 'utf8');
	} catch (err) {
		return { issues: [`cannot read ${path}: ${err.message}`], suggestions: [] };
	}
	// The handler takes the base name; its extension selects component or module.
	const result = await autofixer({
		code,
		desired_svelte_version: 5,
		filename: basename(path),
	});
	return { issues: result.issues, suggestions: result.suggestions };
}

async function main(paths) {
	if (paths.length === 0) {
		console.error('autofixer-gate: no file given');
		return 1;
	}
	let failed = false;
	for (const path of paths) {
		const { issues, suggestions } = await inspectFile(path);
		for (const s of suggestions) console.log(`autofixer-gate: ${path}: suggestion: ${s}`);
		for (const i of issues) console.error(`autofixer-gate: ${path}: issue: ${i}`);
		if (issues.length > 0) failed = true;
		else console.log(`autofixer-gate: ${path}: no issues`);
	}
	return failed ? 1 : 0;
}

if (import.meta.url === pathToFileURL(process.argv[1] ?? '').href) {
	process.exit(await main(process.argv.slice(2)));
}
