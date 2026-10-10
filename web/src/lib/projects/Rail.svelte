<script lang="ts">
	import Folders from '@lucide/svelte/icons/folders';
	import type { ProjectsModel } from './projects';
	import Projects from './Projects.svelte';
	import type { SessionsModel } from './sessions';
	import Spinner from './Spinner.svelte';

	// The rail's projects icon (plan m2d step 7): a badge with the number of agents working
	// and a spinner while any runs. Hovering opens the panel as an overlay over the left
	// panel, a click pins it, Escape closes it. The overlay is positioned against the shell's
	// middle row, so this component renders no wrapper of its own.
	interface Props {
		projects: ProjectsModel;
		sessions: SessionsModel;
		selected: string | null;
		/** Pinned: stays open until a project is chosen, the icon is clicked again or Escape. */
		pinned?: boolean;
	}
	let { projects, sessions, selected, pinned = $bindable(false) }: Props = $props();

	/** The pointer left; a short grace lets it cross the gap between the icon and the overlay. */
	const LEAVE_MS = 180;

	let working = $state(0);
	let hovering = $state(false);
	let leaving: ReturnType<typeof setTimeout> | null = null;
	const open = $derived(hovering || pinned);

	$effect(() => {
		const s = sessions;
		const read = () => {
			working = s.view.runningAgents;
		};
		read();
		return s.subscribe(read);
	});
	$effect(() => () => {
		if (leaving !== null) clearTimeout(leaving);
	});

	function enter() {
		if (leaving !== null) clearTimeout(leaving);
		leaving = null;
		hovering = true;
	}
	function leave() {
		if (leaving !== null) clearTimeout(leaving);
		leaving = setTimeout(() => {
			hovering = false;
			leaving = null;
		}, LEAVE_MS);
	}
	function close() {
		hovering = false;
		pinned = false;
	}
	function onkeydown(e: KeyboardEvent) {
		if (e.key === 'Escape' && open) close();
	}
</script>

<svelte:window {onkeydown} />

<button
	type="button"
	class="rail-button"
	class:current={open}
	aria-expanded={open}
	aria-pressed={pinned}
	aria-label={working > 0 ? `Projects, ${working} agent${working === 1 ? '' : 's'} working` : 'Projects'}
	title="Projects"
	onpointerenter={enter}
	onpointerleave={leave}
	onclick={() => (pinned = !pinned)}
>
	<Folders size={16} />
	{#if working > 0}
		<span class="badge">{working}</span>
		<span class="spin"><Spinner size={9} /></span>
	{/if}
</button>

{#if open}
	<div class="overlay" role="dialog" aria-label="Projects" tabindex="-1" onpointerenter={enter} onpointerleave={leave}>
		<Projects {projects} {sessions} {selected} onchosen={close} />
	</div>
{/if}

<style>
	.rail-button {
		position: relative;
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
	.rail-button.current {
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
	/* Solid: what is behind must not show through the text. */
	.overlay {
		position: absolute;
		z-index: 30;
		top: 0;
		bottom: 0;
		left: 40px;
		width: 300px;
		overflow: auto;
		background: var(--overlay-solid);
		border-right: 1px solid var(--border);
		box-shadow: var(--shadow);
	}
</style>
