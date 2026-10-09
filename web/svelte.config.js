import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';

export default {
	preprocess: vitePreprocess(),
	// Runes mode for the whole app: Svelte 4 syntax is a compile error (ADR-0039 §2).
	compilerOptions: { runes: true },
};
