import { svelte } from '@sveltejs/vite-plugin-svelte';
import { defineConfig } from 'vitest/config';

export default defineConfig({
	plugins: [svelte()],
	build: {
		outDir: 'dist/app',
		emptyOutDir: true,
		// Nothing becomes a data: URI: the CSP refuses them (plan m2a D1).
		assetsInlineLimit: 0,
	},
	test: {
		environment: 'node',
	},
});
