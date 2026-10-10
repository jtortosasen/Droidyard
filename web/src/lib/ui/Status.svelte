<script lang="ts">
	import type { ConnectionState } from '../api/client';
	import type { Result } from '../api/capabilities.gen';

	// The connection and the daemon's health, folded into one compact line for the toolbar
	// or the picker's header. Every field is a text node.
	interface Props {
		connection: ConnectionState;
		health: Result<'system.health'> | null;
		healthError: string | null;
	}
	let { connection, health, healthError }: Props = $props();

	function uptime(seconds: number): string {
		const h = Math.floor(seconds / 3600);
		const m = Math.floor((seconds % 3600) / 60);
		return h > 0 ? `${h} h ${m} min` : m > 0 ? `${m} min ${seconds % 60} s` : `${seconds} s`;
	}
</script>

<span class="status">
	<span class="dot" class:ok={connection === 'open'} class:bad={connection === 'unauthorized'}></span>
	<span>{connection}</span>
	{#if health}
		<span class="muted">
			daemon {health.version ?? 'unknown version'}, up
			{health.uptimeSeconds === undefined ? 'unknown' : uptime(health.uptimeSeconds)}
		</span>
	{/if}
	{#if healthError}
		<span class="err">system.health failed: {healthError}</span>
	{/if}
</span>

<style>
	.status {
		display: inline-flex;
		align-items: center;
		gap: var(--sp-3);
		font-size: var(--fs-small);
		white-space: nowrap;
	}
	.dot {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		background: var(--warning);
	}
	.dot.ok {
		background: var(--success);
	}
	.dot.bad {
		background: var(--danger);
	}
	.muted {
		color: var(--text-2);
	}
	.err {
		color: var(--danger);
	}
</style>
