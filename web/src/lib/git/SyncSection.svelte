<script lang="ts">
	import type { OutcomeRow } from './branch';
	import { callRoots, type SyncOffer } from './branchops';

	// The synchronise part of the Checkout and Create Branch dialogs (plan m2n P8, R2; ADR-0071 §6 as ADR-0074 §3 changed it):
	// the "Synchronise submodules" toggle (`data-sync-toggle`), every repository with whether the call moves it or it keeps
	// its branch and why (`data-sync-roots`; a row's `data-has` and `data-moves`), why the toggle cannot be turned off
	// (`data-sync-forced`: the project lacks the branch) or why nothing can be done (`data-sync-problem`), and, once a call
	// has been made, what each root did (`data-outcome-root` with `data-status`). Every name, cause and next step is git's
	// text: shown as text, never markup.
	interface Props {
		offer: SyncOffer;
		/** Whether the call is applied to the submodules that have the branch too. */
		sync: boolean;
		/** A call is in flight. */
		busy: boolean;
		/** What each root did in the last synchronised call. */
		outcomes: readonly OutcomeRow[];
		/** What the panel calls each root (names.ts); a root without a name is shown by its path. */
		names?: ReadonlyMap<string, string>;
	}
	let { offer, sync = $bindable(), busy, outcomes, names = new Map() }: Props = $props();

	const nameOf = (root: string): string => names.get(root) ?? (root === '.' ? '. (project)' : root);
	const branch = $derived(offer.branch);
	/** The repositories the dialog lists: the project first, then the initialised submodules. */
	const listed = $derived(['.', ...offer.roots]);
	const moves = (root: string): boolean => (root === '.' ? offer.workspaceHas : sync && offer.moves.includes(root));
	const rowText = (root: string): string => {
		const what = branch === '' ? 'the branch' : branch;
		if (root === '.') return offer.workspaceHas ? 'moves' : `stays on its branch: it has no ${what}`;
		if (offer.pending.includes(root)) return 'stays on its branch: its refs are not read yet';
		if (offer.stays.includes(root)) return `stays on its branch: it has no ${what}`;
		if (!sync) return 'stays on its branch: Synchronise is off';
		return offer.atHead.includes(root) ? 'moves, starting at its own HEAD' : 'moves';
	};
	const nothing = $derived(callRoots(offer, sync).length === 0);
</script>

<div class="sync" data-sync>
	<label class="check">
		<input type="checkbox" bind:checked={sync} disabled={busy || offer.forced} data-sync-toggle />
		<span>Synchronise submodules</span>
	</label>
	{#if offer.forced}
		<p class="note" data-sync-forced>The project has no {branch === '' ? 'such branch' : branch}, so only the submodules that have it move; Synchronise stays on.</p>
	{/if}
	<ul class="roots" aria-label="Repositories" data-sync-roots>
		{#each listed as root (root)}
			<li data-sync-root={root} data-has={root === '.' ? offer.workspaceHas : offer.starts.has(root)} data-moves={moves(root)}>
				<span class="path" title={root}>{nameOf(root)}</span>
				<span class="has">{rowText(root)}</span>
			</li>
		{/each}
	</ul>
	{#if nothing}
		<p class="problem" role="status" data-sync-problem>
			{branch === '' ? 'There is no branch to look for.' : `No repository that is read has ${branch}.`} Nothing would change.
		</p>
	{/if}
	{#if outcomes.length > 0}
		<ul class="outcomes" aria-label="Result per repository" data-outcomes>
			{#each outcomes as o (o.root)}
				<li data-outcome-root={o.root} data-status={o.status}>
					<span class="path" title={o.root}>{nameOf(o.root)}</span>
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
	.note {
		margin: 0 0 var(--sp-2);
		color: var(--text-2);
		overflow-wrap: anywhere;
	}
	li[data-moves='true'] .has {
		color: var(--text);
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
		font-size: var(--fs-base);
	}
</style>
