<script lang="ts">
	import { untrack } from 'svelte';
	import { blockText, callRoots, syncOn, targetLabel, type BranchTarget, type SyncOffer } from './branchops';
	import type { BranchModel, BranchView } from './branch';
	import SyncSection from './SyncSection.svelte';

	// The Checkout confirmation (plan m2n P8, R2; ADR-0071 §6, ADR-0074 §3), shown instead of an immediate call when the
	// project has initialised submodules: which branch is checked out, the Synchronise submodules toggle
	// (`data-sync-toggle`; on when a submodule has the branch) with the repositories that move and those that keep their
	// branch, and after the call what each root did
	// (`data-outcome-root`, `data-status`). A checkout stopped by an unsaved editor buffer names the files and
	// calls nothing (`data-dirty-block`); a failed call stays here with git's own words, as text
	// (`data-op-error`). The dialog closes only when the branch is checked out in every root asked. Escape is Cancel.
	interface Props {
		target: BranchTarget;
		offer: SyncOffer;
		model: BranchModel;
		view: BranchView;
		/** Submit: resolves to whether the branch is checked out in every root asked, which closes the dialog. */
		onsubmit: (sync: boolean) => Promise<boolean>;
		oncancel: () => void;
		/** What the panel calls each root (names.ts). */
		names?: ReadonlyMap<string, string>;
	}
	let { target, offer, model, view, onsubmit, oncancel, names }: Props = $props();

	/** What the person chose for Synchronise; null until they touch it, when the offer's default (a submodule has the branch, ADR-0074 §3) applies, as the refs arrive. */
	let syncChoice = $state<boolean | null>(null);
	const sync = $derived(syncOn(syncChoice, offer));
	/** The unsaved files that stop a checkout, read when the dialog opens and at submit. */
	let blocked = $state.raw<readonly string[]>(untrack(() => model.dirtyFor(true)));
	/** No repository would be checked out: nothing to call. */
	const syncBlocked = $derived(callRoots(offer, sync).length === 0);

	async function submit(e: SubmitEvent) {
		e.preventDefault();
		blocked = model.dirtyFor(true);
		if (blocked.length > 0 || syncBlocked || view.busy) return;
		await onsubmit(sync);
	}

	/** Opens the dialog as a modal. */
	function modal(el: HTMLDialogElement) {
		el.showModal();
		return () => {
			if (el.open) el.close();
		};
	}
</script>

<dialog
	class="checkout"
	aria-labelledby="checkout-title"
	data-checkout-dialog
	{@attach modal}
	oncancel={(e) => {
		e.preventDefault();
		if (!view.busy) oncancel();
	}}
>
	<form onsubmit={submit}>
		<h2 id="checkout-title">Check out</h2>
		<p class="what" data-checkout-branch>
			Check out <span class="ref">{targetLabel(target)}</span>
		</p>
		<SyncSection {offer} {names} bind:sync={() => sync, (v) => (syncChoice = v)} busy={view.busy} outcomes={view.outcomes} />
		{#if blocked.length > 0}
			<div class="problem" role="alert" data-dirty-block>
				<p>{blockText(blocked)} Cancel to save them first.</p>
				<ul aria-label="Files with unsaved changes">
					{#each blocked as path (path)}
						<li>{path}</li>
					{/each}
				</ul>
			</div>
		{/if}
		{#if view.failure}
			<div class="problem" role="alert" data-op-error>
				<p>{view.failure.code ? `${view.failure.code}: ` : ''}{view.failure.message}</p>
				{#if view.failure.cause}<pre class="cause">{view.failure.cause}</pre>{/if}
				{#if view.failure.nextStep}<p>{view.failure.nextStep}</p>{/if}
			</div>
		{/if}
		<div class="actions">
			<button type="button" data-cancel disabled={view.busy} onclick={oncancel}>Cancel</button>
			<button type="submit" data-submit disabled={view.busy || syncBlocked}>{view.busy ? 'Checking out…' : 'Check out'}</button>
		</div>
	</form>
</dialog>

<style>
	.checkout {
		width: min(460px, calc(100vw - 2 * var(--sp-4)));
		padding: var(--sp-4);
		border: 1px solid var(--border);
		border-radius: var(--radius-panel);
		background: var(--overlay-solid);
		color: var(--text);
		box-shadow: var(--shadow);
	}
	.checkout::backdrop {
		background: var(--scrim);
	}
	/* The dialog sits inside a pane whose own h2 is visually hidden (Shell.svelte); this one is
	   the dialog's visible title (the id outweighs that rule). */
	#checkout-title {
		position: static;
		width: auto;
		height: auto;
		overflow: visible;
		clip-path: none;
		margin: 0 0 var(--sp-3);
		font-size: var(--fs-base);
	}
	p {
		margin: 0 0 var(--sp-2);
	}
	.what {
		color: var(--text-2);
	}
	.ref {
		unicode-bidi: isolate;
		color: var(--text);
		overflow-wrap: anywhere;
	}
	.problem {
		margin-bottom: var(--sp-3);
		color: var(--danger);
		overflow-wrap: anywhere;
	}
	.problem ul {
		margin: 0;
		padding-left: var(--sp-5, 20px);
		max-height: 30vh;
		overflow: auto;
	}
	.cause {
		margin: 0 0 var(--sp-2);
		white-space: pre-wrap;
		overflow-wrap: anywhere;
		font-family: var(--font-mono);
		font-size: var(--fs-base);
	}
	.actions {
		display: flex;
		justify-content: flex-end;
		gap: var(--sp-2);
	}
</style>
