<script lang="ts">
	import { untrack } from 'svelte';
	import { blockText, callRoots, nameProblem, syncOn, type NewBranchSource, type SyncOffer } from './branchops';
	import type { BranchModel, BranchView } from './branch';
	import SyncSection from './SyncSection.svelte';

	// The Create Branch dialog (plan m2n P3; ADR-0071 §1, §5), a modal as the unsaved-changes dialog
	// is: the new branch's name, where it starts (the branch the menu was opened on, or HEAD, with
	// its short oid), and Check out, on by default. With Check out on and an unsaved editor buffer
	// the dialog names the files and calls nothing (`data-dirty-block`; Create asks again, so files
	// saved meanwhile no longer block); with it off nothing is blocked, since no file is touched. A failed call stays here with git's own words, as text
	// (`data-op-error`); the dialog closes only when the branch is made. Escape is Cancel.
	// With initialised submodules (`offer`, plan m2n P8, R2; ADR-0071 §6, ADR-0074 §3) it also holds the Synchronise submodules
	// toggle, on when a submodule has the branch the start is, the repositories that get the branch and those that do not, and,
	// after a call, what each root did.
	interface Props {
		source: NewBranchSource;
		/** The local branches that exist, so the name can be refused before the call. */
		locals: ReadonlySet<string>;
		model: BranchModel;
		view: BranchView;
		/** The submodules the branch can also be made in; null with none. */
		offer?: SyncOffer | null;
		/** Submit: resolves to whether the branch was made (in every root asked), which closes the dialog. */
		onsubmit: (name: string, checkout: boolean, sync: boolean) => Promise<boolean>;
		oncancel: () => void;
		/** What the panel calls each root (names.ts). */
		names?: ReadonlyMap<string, string>;
	}
	let { source, locals, model, view, offer = null, onsubmit, oncancel, names }: Props = $props();

	let name = $state(untrack(() => source.proposed));
	let checkout = $state(true);
	/** What the person chose for Synchronise; null until they touch it, when the offer's default (a submodule has the start's branch, ADR-0074 §3) applies, as the refs arrive. */
	let syncChoice = $state<boolean | null>(null);
	const sync = $derived(syncOn(syncChoice, offer));
	/** The unsaved files that stop a checkout, read when the dialog opens, when Check out changes and at submit. */
	let blocked = $state.raw<readonly string[]>(untrack(() => model.dirtyFor(true)));
	const problem = $derived(nameProblem(name, locals));
	/** No repository would get the branch: nothing to call. */
	const syncBlocked = $derived(offer !== null && callRoots(offer, sync).length === 0);

	async function submit(e: SubmitEvent) {
		e.preventDefault();
		blocked = model.dirtyFor(checkout);
		if (blocked.length > 0 || problem !== null || syncBlocked || view.busy) return;
		await onsubmit(name.trim(), checkout, sync);
	}

	/** Opens the dialog as a modal and puts the cursor in the name. */
	function modal(el: HTMLDialogElement) {
		el.showModal();
		el.querySelector<HTMLInputElement>('[data-name-input]')?.focus();
		return () => {
			if (el.open) el.close();
		};
	}
</script>

<dialog
	class="create-branch"
	aria-labelledby="create-branch-title"
	data-create-branch-dialog
	{@attach modal}
	oncancel={(e) => {
		e.preventDefault();
		if (!view.busy) oncancel();
	}}
>
	<form onsubmit={submit}>
		<h2 id="create-branch-title">Create branch</h2>
		<p class="from" data-from>
			From <span class="ref" data-from-ref>{source.label}</span> <span class="oid" data-from-oid>{source.oid.slice(0, 7)}</span>
		</p>
		<label class="field">
			<span>Name</span>
			<input type="text" bind:value={name} data-name-input autocomplete="off" spellcheck="false" aria-invalid={problem !== null && name.trim() !== ''} />
		</label>
		{#if problem !== null && name.trim() !== ''}
			<p class="problem" role="status" data-name-problem>{problem}</p>
		{/if}
		<label class="check">
			<input type="checkbox" bind:checked={checkout} data-checkout-toggle onchange={(e) => (blocked = model.dirtyFor(e.currentTarget.checked))} />
			<span>Check out</span>
		</label>
		{#if offer !== null}
			<SyncSection {offer} {names} bind:sync={() => sync, (v) => (syncChoice = v)} busy={view.busy} outcomes={view.outcomes} />
		{/if}
		{#if blocked.length > 0}
			<div class="problem" role="alert" data-dirty-block>
				<p>{blockText(blocked)} Cancel to save them, or turn Check out off.</p>
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
			<button type="submit" data-submit disabled={view.busy || problem !== null || syncBlocked}>{view.busy ? 'Creating…' : 'Create'}</button>
		</div>
	</form>
</dialog>

<style>
	.create-branch {
		width: min(460px, calc(100vw - 2 * var(--sp-4)));
		padding: var(--sp-4);
		border: 1px solid var(--border);
		border-radius: var(--radius-panel);
		background: var(--overlay-solid);
		color: var(--text);
		box-shadow: var(--shadow);
	}
	.create-branch::backdrop {
		background: var(--scrim);
	}
	/* The dialog sits inside a pane whose own h2 is visually hidden (Shell.svelte); this one is
	   the dialog's visible title (the id outweighs that rule). */
	#create-branch-title {
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
	.from {
		color: var(--text-2);
	}
	.ref {
		unicode-bidi: isolate;
		color: var(--text);
		overflow-wrap: anywhere;
	}
	.oid {
		font-family: var(--font-mono);
		color: var(--text-3);
	}
	.field {
		display: flex;
		flex-direction: column;
		gap: var(--sp-1);
		margin-bottom: var(--sp-2);
	}
	.field input {
		width: 100%;
	}
	.check {
		display: flex;
		align-items: center;
		gap: var(--sp-2);
		margin-bottom: var(--sp-3);
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
		font-size: var(--fs-small);
	}
	.actions {
		display: flex;
		justify-content: flex-end;
		gap: var(--sp-2);
	}
</style>
