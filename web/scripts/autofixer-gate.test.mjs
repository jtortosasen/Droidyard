import { mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterAll, describe, expect, it } from 'vitest';
import { inspectFile } from './autofixer-gate.mjs';

// These run the real, pinned svelteAutofixer: the gate rests on it reporting what
// svelte-check accepts (plan m2a step 1, "Rests on").
const dir = mkdtempSync(join(tmpdir(), 'autofixer-gate-'));
afterAll(() => rmSync(dir, { recursive: true, force: true }));

function write(name, code) {
	const path = join(dir, name);
	writeFileSync(path, code);
	return path;
}

describe('autofixer gate', () => {
	// Decision (ADR-0039 §3): `issues` fail the gate. An each block without a key is
	// the seed svelte-check passes and the handler reports.
	it('reports an each block without a key as an issue', async () => {
		const path = write(
			'Each.svelte',
			'<script lang="ts">\n\tlet items = $state(["a"]);\n</script>\n\n{#each items as item}\n\t<p>{item}</p>\n{/each}\n',
		);
		const { issues } = await inspectFile(path);
		expect(issues.join('\n')).toContain('key');
	});

	it('reports Svelte 4 syntax as an issue', async () => {
		const path = write('Legacy.svelte', '<script lang="ts">\n\texport let x: number;\n</script>\n\n<p>{x}</p>\n');
		expect((await inspectFile(path)).issues).not.toEqual([]);
	});

	it('passes a clean component', async () => {
		const path = write('Clean.svelte', '<script lang="ts">\n\tlet n = $state(0);\n</script>\n\n<button onclick={() => n++}>{n}</button>\n');
		expect((await inspectFile(path)).issues).toEqual([]);
	});

	// Decision: the extension selects component or module, so a rune module is
	// analysed as a module and a clean one passes.
	it('analyses a .svelte.ts module as a module', async () => {
		const bad = write(
			'bad.svelte.ts',
			'export function counter() {\n\tlet count = $state(0);\n\treturn { count };\n}\n',
		);
		expect((await inspectFile(bad)).issues).not.toEqual([]);
		const ok = write(
			'ok.svelte.ts',
			'export function counter() {\n\tlet count = $state(0);\n\treturn {\n\t\tget count() {\n\t\t\treturn count;\n\t\t},\n\t};\n}\n',
		);
		expect((await inspectFile(ok)).issues).toEqual([]);
	});

	// Decision: a path that does not exist fails the gate rather than passing silently.
	it('fails on a missing file', async () => {
		const { issues } = await inspectFile(join(dir, 'Nope.svelte'));
		expect(issues).toHaveLength(1);
		expect(issues[0]).toContain('cannot read');
	});

	// Suggestions are printed, not fatal (ADR-0039 §3 names issues only).
	it('does not treat suggestions as issues', async () => {
		const path = write(
			'Suggest.svelte.ts',
			'import { writable } from "svelte/store";\nexport const s = writable(1);\n',
		);
		const r = await inspectFile(path);
		expect(r.suggestions).not.toEqual([]);
		expect(r.issues).toEqual([]);
	});
});
