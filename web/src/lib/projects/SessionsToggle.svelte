<script lang="ts">
	import Folders from '@lucide/svelte/icons/folders';
	import type { SessionsModel } from './sessions';
	import Spinner from './Spinner.svelte';

	// The toolbar's one sessions toggle (plan m2e D33, ADR-0050): it opens and closes the
	// column docked left of the tree, and carries the number of agents working and a
	// spinner while any runs. A click only: no hover reveal, no Escape.
	interface Props {
		sessions: SessionsModel;
		open: boolean;
		ontoggle: () => void;
	}
	let { sessions, open, ontoggle }: Props = $props();

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
		background: transparent;
		color: var(--text-2);
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
