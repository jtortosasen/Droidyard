<script lang="ts">
	import type { Answer, Pending } from './unsaved';

	// The unsaved-changes dialog (plan m2i D7), as CollisionDialog: the files in question as
	// text and three answers. Escape is Cancel; nothing is lost without the user's word.
	interface Props {
		pending: Pending;
		onanswer: (answer: Answer) => void;
	}
	let { pending, onanswer }: Props = $props();

	/** Opens the dialog as a modal; the first button has the focus. */
	function modal(el: HTMLDialogElement) {
		el.showModal();
		return () => {
			if (el.open) el.close();
		};
	}
</script>

<dialog
	class="unsaved"
	aria-labelledby="unsaved-title"
	{@attach modal}
	oncancel={(e) => {
		e.preventDefault();
		if (!pending.busy) onanswer('cancel');
	}}
>
	<h2 id="unsaved-title">Unsaved changes</h2>
	<p>{pending.paths.length === 1 ? 'This file has' : 'These files have'} changes that are not saved:</p>
	<ul aria-label="Files with unsaved changes">
		{#each pending.paths as path (path)}
			<li>{path}</li>
		{/each}
	</ul>
	{#if pending.error}
		<p class="error" role="alert">Not saved: {pending.error}</p>
	{/if}
	<div class="actions">
		<button type="button" data-action="save" disabled={pending.busy} onclick={() => onanswer('save')}>Save</button>
		<button type="button" data-action="discard" disabled={pending.busy} onclick={() => onanswer('discard')}>Don't save</button>
		<button type="button" data-action="cancel" disabled={pending.busy} onclick={() => onanswer('cancel')}>Cancel</button>
	</div>
</dialog>

<style>
	.unsaved {
		max-width: min(460px, calc(100vw - 2 * var(--sp-4)));
		padding: var(--sp-4);
		border: 1px solid var(--border);
		border-radius: var(--radius-panel);
		background: var(--overlay-solid);
		color: var(--text);
		box-shadow: var(--shadow);
	}
	.unsaved::backdrop {
		background: rgb(0 0 0 / 0.4);
	}
	h2 {
		margin: 0 0 var(--sp-3);
		font-size: var(--fs-base);
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
	.error {
		color: var(--danger);
	}
	.actions {
		display: flex;
		justify-content: flex-end;
		gap: var(--sp-2);
	}
</style>
