<script lang="ts">
	import Folder from '@lucide/svelte/icons/folder';
	import GitBranch from '@lucide/svelte/icons/git-branch';
	import type { ConnectionState } from '../api/client';
	import type { Result } from '../api/capabilities.gen';
	import type { TabWhere } from '../start/title';
	import { formatUptime, liveUptime } from './uptime';

	// The connection and the daemon's health, folded into one compact line for the toolbar
	// or the projects page's header. Every field is a text node.
	interface Props {
		connection: ConnectionState;
		health: Result<'system.health'> | null;
		healthError: string | null;
		/** The tab's worktree; absent on the page with no project. */
		where?: TabWhere | null;
	}
	let { connection, health, healthError, where = null }: Props = $props();

	// system.health answers once: the uptime shown advances from the moment it arrived.
	let receivedAt = $state(Date.now());
	let now = $state(Date.now());
	$effect(() => {
		if (health === null) return;
		receivedAt = now = Date.now();
		const timer = setInterval(() => (now = Date.now()), 1000);
		return () => clearInterval(timer);
	});
</script>

<span class="status">
	{#if where}
		<span class="tree" title={where.tree}>
			{#if where.primary}<Folder size={12} aria-hidden="true" />{:else}<GitBranch size={12} aria-hidden="true" />{/if}
			<span class="name">{where.tree}</span>
			{#if where.primary}<span class="tag">primary</span>{/if}
		</span>
	{/if}
	<span class="dot" class:ok={connection === 'open'} class:bad={connection === 'unauthorized'}></span>
	<span>{connection}</span>
	{#if health}
		<span class="muted">
			daemon {health.version ?? 'unknown version'}, up
			{health.uptimeSeconds === undefined ? 'unknown' : formatUptime(liveUptime(health.uptimeSeconds, receivedAt, now))}
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
	.tree {
		display: inline-flex;
		align-items: center;
		gap: var(--sp-2);
		min-width: 0;
		max-width: 24ch;
	}
	.tree :global(svg) {
		flex: none;
	}
	.name {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.tag {
		flex: none;
		color: var(--text-2);
	}
	.muted {
		color: var(--text-2);
	}
	.err {
		color: var(--danger);
	}
</style>
