<script module lang="ts">
	export type ClientKind = 'claude-code' | 'cursor' | 'cursor-agent' | 'other';
	const NAMES: Record<ClientKind, string> = { 'claude-code': 'Claude Code', cursor: 'Cursor', 'cursor-agent': 'cursor-agent', other: 'Other' };
	const HUES: Record<ClientKind, string> = { 'claude-code': 'claude', cursor: 'cursor', 'cursor-agent': 'agent', other: 'other' };
</script>

<script lang="ts">
	import Spinner from './Spinner.svelte';

	// A client's tag (ADR-0134 §6): its name on its own soft hue, so colour is never the only
	// carrier. `label` replaces the name for a live connection (its numbered label), `connected`
	// marks a connection with a dot, `running` with a spinner. Every string is a text node.
	interface Props {
		kind: ClientKind;
		label?: string;
		connected?: boolean;
		running?: boolean;
		title?: string;
	}
	let { kind, label, connected = false, running = false, title }: Props = $props();
</script>

<span class="tag" data-hue={HUES[kind]} {title}>
	{#if running}
		<Spinner size={9} />
	{:else if connected}
		<span class="live" aria-hidden="true"></span>
	{/if}
	<span class="text">{label ?? NAMES[kind]}</span>
</span>

<style>
	.tag {
		--hue: var(--client-other);
		--soft: var(--client-other-soft);
		display: inline-flex;
		align-items: center;
		gap: var(--sp-2);
		flex: none;
		max-width: 100%;
		padding: 0 var(--sp-3);
		border-radius: 3px;
		background: var(--soft);
		color: var(--hue);
		font-size: var(--fs-label);
		font-weight: 500;
		line-height: var(--lh-base);
		white-space: nowrap;
	}
	.tag[data-hue='claude'] {
		--hue: var(--client-claude);
		--soft: var(--client-claude-soft);
	}
	.tag[data-hue='cursor'] {
		--hue: var(--client-cursor);
		--soft: var(--client-cursor-soft);
	}
	.tag[data-hue='agent'] {
		--hue: var(--client-agent);
		--soft: var(--client-agent-soft);
	}
	.text {
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.live {
		width: 6px;
		height: 6px;
		flex: none;
		border-radius: 50%;
		background: var(--success);
	}
</style>
