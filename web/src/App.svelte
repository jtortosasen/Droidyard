<script lang="ts">
	import { Client, type ConnectionState } from './lib/api/client';
	import type { Result } from './lib/api/capabilities.gen';
	import { bootstrapToken, forgetToken } from './lib/api/token';

	// The token comes from the fragment `droidyard ui` opened, once; a tab without one,
	// or whose token the daemon refused, shows the instruction (http-websocket.md
	// §Security baseline).
	const token = bootstrapToken({
		location: window.location,
		history: window.history,
		storage: window.sessionStorage,
	});

	let connection = $state<ConnectionState>('connecting');
	let health = $state.raw<Result<'system.health'> | null>(null);
	let healthError = $state<string | null>(null);

	$effect(() => {
		if (token === null) return;
		const client = new Client({
			url: `ws://${window.location.host}/v1/ws`,
			token,
			createSocket: (url) => new WebSocket(url),
			onState: (s) => {
				connection = s;
				if (s === 'unauthorized') forgetToken(window.sessionStorage);
				if (s === 'open') refresh();
			},
		});
		function refresh() {
			client.call('system.health', {}).then(
				(h) => {
					health = h;
					healthError = null;
				},
				(e: unknown) => {
					// A lost connection is shown by the state; a capability error by its code.
					if (e instanceof Error && 'code' in e) healthError = String(e.code);
				},
			);
		}
		client.connect();
		return () => client.close();
	});

	function uptime(seconds: number): string {
		const h = Math.floor(seconds / 3600);
		const m = Math.floor((seconds % 3600) / 60);
		return h > 0 ? `${h} h ${m} min` : m > 0 ? `${m} min ${seconds % 60} s` : `${seconds} s`;
	}
</script>

<h1>Droidyard</h1>

{#if token === null}
	<p>No token in this tab: run <code>droidyard ui</code>.</p>
{:else if connection === 'unauthorized'}
	<p>The daemon restarted: run <code>droidyard ui</code>.</p>
{:else}
	<p>Connection: <strong>{connection}</strong></p>
	{#if health}
		<p>
			Daemon {health.version ?? 'unknown version'}, up
			{health.uptimeSeconds === undefined ? 'unknown' : uptime(health.uptimeSeconds)}
		</p>
	{/if}
	{#if healthError}
		<p>system.health failed: {healthError}</p>
	{/if}
{/if}

<style>
	:global(body) {
		font-family: system-ui, sans-serif;
		margin: 1rem;
		line-height: 1.4;
	}
</style>
