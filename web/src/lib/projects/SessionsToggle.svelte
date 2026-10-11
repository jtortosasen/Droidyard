<script lang="ts">
	import Folders from '@lucide/svelte/icons/folders';
	import type { SessionsModel } from './sessions';
	import Spinner from './Spinner.svelte';

	// The toolbar's one sessions toggle (plan m2e D33, ADR-0050, ADR-0052): a click docks and
	// undocks the column; resting on it or pushing the pointer into the window's left edge
	// shows the column as an overlay instead, and `attract` (0..1) tints the button as the
	// pointer nears that edge. It carries the number of agents working and a spinner while
	// any runs.
	interface Props {
		sessions: SessionsModel;
		open: boolean;
		ontoggle: () => void;
		/** 0..1: how near the pointer is to the left edge (the viscous edge, D44). */
		attract?: number;
		/** Hands the shell the button, whose rect says when the pointer rests on it. */
		register?: (el: HTMLElement | null) => void;
	}
	let { sessions, open, ontoggle, attract = 0, register }: Props = $props();

	let working = $state(0);

	$effect(() => {
		const s = sessions;
		const read = () => {
			working = s.view.runningAgents;
		};
		read();
		return s.subscribe(read);
	});
</script>

<button
	type="button"
	class="toggle"
	class:current={open}
	aria-expanded={open}
	aria-label={working > 0 ? `Sessions, ${working} agent${working === 1 ? '' : 's'} working` : 'Sessions'}
	title="Sessions"
	style:--attract={attract}
	{@attach (node) => {
		register?.(node);
		return () => register?.(null);
	}}
	onclick={ontoggle}
>
	<Folders size={16} />
	{#if working > 0}
		<span class="badge">{working}</span>
		<span class="spin"><Spinner size={9} /></span>
	{/if}
</button>

<style>
	.toggle {
		position: relative;
		flex: none;
		display: grid;
		place-items: center;
		width: 30px;
		height: 30px;
		padding: 0;
		border: 0;
		border-radius: var(--radius-control);
		color: color-mix(in srgb, var(--accent) calc(var(--attract) * 100%), var(--text-2));
		background: color-mix(in srgb, var(--accent-soft) calc(var(--attract) * 100%), transparent);
		box-shadow: 0 0 calc(var(--attract) * 8px) color-mix(in srgb, var(--accent) calc(var(--attract) * 60%), transparent);
	}
	.toggle.current {
		background: var(--accent-soft);
		color: var(--accent);
	}
	.badge {
		position: absolute;
		top: -3px;
		right: -5px;
		min-width: 14px;
		height: 14px;
		padding: 0 var(--sp-2);
		border-radius: 7px;
		background: var(--accent);
		color: var(--accent-text);
		font-size: 9px;
		line-height: 14px;
		text-align: center;
	}
	.spin {
		position: absolute;
		bottom: 0;
		right: 0;
		display: inline-flex;
	}
</style>
