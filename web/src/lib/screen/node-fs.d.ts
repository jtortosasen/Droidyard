// The one Node call the golden-file test makes; the web code has no Node types otherwise.
declare module 'node:fs' {
	export function readFileSync(path: URL): Uint8Array;
}
