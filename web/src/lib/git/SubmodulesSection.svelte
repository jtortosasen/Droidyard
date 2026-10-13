<script lang="ts">
	import ChevronDown from '@lucide/svelte/icons/chevron-down';
	import ChevronRight from '@lucide/svelte/icons/chevron-right';
	import Package from '@lucide/svelte/icons/package';
	import { submoduleRows, type SubmodulesView } from './submodules';

	// The side panel's SUBMODULES section (plan m2n P4), after SourceGit's, between TAGS and WORKTREES: its
	// count is the rows that pass the panel's filter; a row has the submodule's path, its state and its
	// short commit (the checked-out one then the recorded one when they differ). Paths and oids are git's
	// text, in an isolated text node. Update is the next step's (P5).
	interface Props {
		view: SubmodulesView;
		/** The filter box above the sections. */
		filter: string;
		/** Reads the section again after a failed first read. */
		onretry: () => void;
	}
	let { view, filter, onretry }: Props = $props();

	let open = $state(true);
	const rows = $derived(view.state === 'ready' ? submoduleRows(view.list, filter) : []);
</script>

<section class="sec" data-refs-section="submodules" data-count={rows.length}>
	<h3>
		<button type="button" class="sec-head" aria-expanded={open} onclick={() => (open = !open)}>
			{#if open}<ChevronDown size={12} />{:else}<ChevronRight size={12} />{/if}
			<span class="title">SUBMODULES</span>
			<span class="n">{rows.length}</span>
		</button>
	</h3>
	{#if open}
		{#if view.state === 'error'}
			<div class="problem" role="alert">
				<p>{view.problem}</p>
				<button type="button" onclick={onretry}>Retry</button>
			</div>
		{:else if view.state !== 'ready'}
			<p class="note" role="status">Reading the submodules…</p>
		{:else}
			<div class="rows" role="tree" aria-label="SUBMODULES">
				{#each rows as row (row.path)}
					<div
						class="row"
						role="treeitem"
						aria-selected="false"
						aria-level="1"
						tabindex="-1"
						title={row.title}
						data-submodule={row.path}
						data-state={row.state}
						data-recorded={row.recorded}
						data-checked-out={row.checkedOut}
					>
						<Package size={13} />
						<span class="text">{row.path}</span>
						<span class="state" data-state-mark>{row.state}</span>
						<span class="oids">{row.oids}</span>
					</div>
				{/each}
				{#if rows.length === 0}
					<p class="note">{filter.trim() === '' ? 'None' : 'No match'}</p>
				{/if}
			</div>
			{#if view.truncated}
				<p class="note" role="status">Some submodules are not listed (the repository has more than the daemon returns).</p>
			{/if}
		{/if}
	{/if}
</section>

<style>
	.sec h3 {
		margin: 0;
		font-size: var(--fs-small);
	}
	.sec-head {
		display: flex;
		align-items: center;
		gap: var(--sp-3);
		width: 100%;
		min-height: var(--row);
		border: 0;
		border-radius: 0;
		background: var(--bg-raised);
		color: var(--text-2);
		font-size: var(--fs-small);
		font-weight: 600;
		letter-spacing: 0.04em;
		text-align: left;
	}
	.title {
		flex: 1;
	}
	.n {
		margin-left: auto;
		font-family: var(--font-mono);
		font-size: var(--fs-small);
		font-weight: 400;
		color: var(--text-3);
	}
	.row {
		display: flex;
		align-items: center;
		gap: var(--sp-3);
		min-height: var(--row);
		padding: 0 var(--sp-3) 0 var(--sp-4);
		color: var(--text);
		cursor: default;
	}
	.row:hover {
		background: var(--bg-hover);
	}
	.row > :global(svg:first-child) {
		flex: none;
		color: var(--text-3);
	}
	.text {
		unicode-bidi: isolate;
		flex: 1 1 0;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.state {
		flex: none;
		font-size: var(--fs-small);
		color: var(--text-3);
	}
	.row[data-state='clean'] .state {
		color: var(--success);
	}
	.row[data-state='outdated'] .state {
		color: var(--warning);
	}
	.row[data-state='dirty'] .state {
		color: var(--danger);
	}
	.oids {
		unicode-bidi: isolate;
		flex: none;
		font-family: var(--font-mono);
		font-size: var(--fs-small);
		color: var(--text-3);
	}
	.note {
		margin: 0;
		padding: var(--sp-2) var(--sp-4);
		font-size: var(--fs-small);
		color: var(--text-3);
	}
	.problem {
		padding: var(--sp-3) var(--sp-4);
		color: var(--danger);
	}
	.problem p {
		margin: 0 0 var(--sp-3);
		overflow-wrap: anywhere;
	}
</style>
