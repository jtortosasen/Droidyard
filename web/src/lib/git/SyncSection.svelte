<script lang="ts">
	import type { OutcomeRow } from './branch';
	import type { SyncOffer } from './branchops';

	// The synchronise part of the Checkout and Create Branch dialogs (plan m2n P8; ADR-0071 §6):
	// the "Synchronise submodules" toggle (`data-sync-toggle`), the initialised submodules it would
	// apply the call to with whether each has the branch (`data-sync-roots`), why it cannot be on
	// (`data-sync-problem`), and, once a call has been made, what each root did (`data-outcome-root`
	// with `data-status`). Every name, cause and next step is git's text: shown as text, never markup.
	interface Props {
		offer: SyncOffer;
		/** Whether the call is applied to the submodules too. */
		sync: boolean;
		/** A call is in flight. */
		busy: boolean;
		/** What each root did in the last synchronised call. */
		outcomes: readonly OutcomeRow[];
	}
	let { offer, sync = $bindable(), busy, outcomes }: Props = $props();

	const rootText = (root: string): string => (root === '.' ? '. (project)' : root);
	const hasText = (root: string): string => (offer.branch === '' ? 'no branch to look for' : offer.starts.has(root) ? `has ${offer.branch}` : `has no ${offer.branch}`);
</script>

<div class="sync" data-sync>
	<label class="check">
		<input type="checkbox" bind:checked={sync} disabled={busy} data-sync-toggle />
		<span>Synchronise submodules</span>
	</label>
	<ul class="roots" aria-label="Submodules" data-sync-roots>
		{#each offer.roots as root (root)}
			<li data-sync-root={root} data-has={offer.starts.has(root)}>
				<span class="path">{root}</span>
				<span class="has">{hasText(root)}</span>
			</li>
		{/each}
	</ul>
	{#if sync && offer.missing.length > 0}
		<p class="problem" role="status" data-sync-problem>
			{offer.branch === '' ? 'HEAD is on no branch, so there is none to look for in the submodules.' : `${offer.branch} is not in every submodule: ${offer.missing.join(', ')}.`} Turn Synchronise off to apply it to the project alone.
		</p>
	{/if}
	{#if outcomes.length > 0}
		<ul class="outcomes" aria-label="Result per repository" data-outcomes>
			{#each outcomes as o (o.root)}
				<li data-outcome-root={o.root} data-status={o.status}>
					<span class="path">{rootText(o.root)}</span>
					<span class="status">{o.status}</span>
					{#if o.failure}
						<div class="why">
							<p>{o.failure.code ? `${o.failure.code}: ` : ''}{o.failure.message}</p>
							{#if o.failure.cause}<pre class="cause">{o.failure.cause}</pre>{/if}
							{#if o.failure.nextStep}<p>{o.failure.nextStep}</p>{/if}
						</div>
					{/if}
				</li>
			{/each}
		</ul>
	{/if}
</div>

<style>
	.sync {
		margin-bottom: var(--sp-3);
	}
	.check {
		display: flex;
		align-items: center;
		gap: var(--sp-2);
		margin-bottom: var(--sp-2);
	}
	ul {
		margin: 0 0 var(--sp-2);
		padding: 0;
		list-style: none;
		max-height: 30vh;
		overflow: auto;
	}
	li {
		display: flex;
		flex-wrap: wrap;
		gap: var(--sp-2);
		padding: 2px 0;
	}
	.path {
		unicode-bidi: isolate;
		font-family: var(--font-mono);
		overflow-wrap: anywhere;
	}
	.has,
	.status {
		color: var(--text-2);
	}
	.outcomes li[data-status='failed'] .status {
		color: var(--danger);
	}
	.problem {
		margin: 0 0 var(--sp-2);
		color: var(--danger);
		overflow-wrap: anywhere;
	}
	.why {
		flex-basis: 100%;
		color: var(--danger);
		overflow-wrap: anywhere;
	}
	.why p {
		margin: 0 0 var(--sp-1);
	}
	.cause {
		margin: 0 0 var(--sp-1);
		white-space: pre-wrap;
		overflow-wrap: anywhere;
		font-family: var(--font-mono);
		font-size: var(--fs-small);
	}
</style>
