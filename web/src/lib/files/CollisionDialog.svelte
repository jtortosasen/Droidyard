<script lang="ts">
	import type { Collision } from './ops';

	// The name-collision dialog (plan m2g step 7; D54): the names in the way, as text, and
	// two answers. Escape is Skip. There is no Replace in this milestone.
	interface Props {
		pending: Collision;
		onkeep: () => void;
		onskip: () => void;
	}
	let { pending, onkeep, onskip }: Props = $props();

	/** Opens the dialog as a modal; the first button has the focus. */
	function modal(el: HTMLDialogElement) {
		el.showModal();
		return () => {
			if (el.open) el.close();
		};
	}
</script>

<dialog
	class="collision"
	aria-labelledby="collision-title"
	{@attach modal}
	oncancel={(e) => {
		e.preventDefault();
		onskip();
	}}
>
	<h2 id="collision-title">Name already taken</h2>
	<p>{pending.names.length === 1 ? 'This name exists' : 'These names exist'} in the destination:</p>
	<ul aria-label="Names in the way">
		{#each pending.names as name, i (i)}
			<li>{name}</li>
		{/each}
	</ul>
	<div class="actions">
		<button type="button" data-action="keep-both" onclick={onkeep}>Keep both</button>
		<button type="button" data-action="skip" onclick={onskip}>Skip</button>
	</div>
</dialog>

<style>
	.collision {
		max-width: min(420px, calc(100vw - 2 * var(--sp-4)));
		padding: var(--sp-4);
		border: 1px solid var(--border);
		border-radius: var(--radius-panel);
		background: var(--overlay-solid);
		color: var(--text);
		box-shadow: var(--shadow);
	}
	.collision::backdrop {
		background: var(--scrim);
	}
	/* The dialog sits inside a pane whose own h2 is visually hidden (Shell.svelte); this
	   one is the dialog's visible title (the id outweighs that rule). */
	#collision-title {
		position: static;
		width: auto;
		height: auto;
		overflow: visible;
		clip-path: none;
		margin: 0 0 var(--sp-3);
	}
	p {
		margin: 0 0 var(--sp-2);
	}
	ul {
		margin: 0 0 var(--sp-4);
		padding-left: var(--sp-5, 20px);
		max-height: 40vh;
		overflow: auto;
		overflow-wrap: anywhere;
	}
	.actions {
		display: flex;
		justify-content: flex-end;
		gap: var(--sp-2);
	}
</style>
