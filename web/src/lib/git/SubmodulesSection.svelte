<script lang="ts">
	import ChevronDown from '@lucide/svelte/icons/chevron-down';
	import ChevronRight from '@lucide/svelte/icons/chevron-right';
	import Package from '@lucide/svelte/icons/package';
	import RefreshCw from '@lucide/svelte/icons/refresh-cw';
	import X from '@lucide/svelte/icons/x';
	import Spinner from '../projects/Spinner.svelte';
	import type { RefsResult } from './refs';
	import { summaryText, type SubmoduleUpdateModel, type UpdateView } from './subupdate';
	import { submoduleRows, type SubmodulesView } from './submodules';

	// The side panel's SUBMODULES section (plan m2n P4), after SourceGit's, between TAGS and WORKTREES: its
	// count is the rows that pass the panel's filter. A row (plan m2n R1) has the submodule's display name (the full
	// path in its title), its state and, under them, the branch it has checked out (or "detached" and the branches
	// at its commit), the short commit (the checked-out one then the recorded one when they differ) and the
	// commit's subject. Paths, names, branches and subjects are git's text, in an isolated text node. Update (plan m2n P5) is
	// the section's (every submodule) and each row's: one update at a time, running and Cancel as the toolbar's
	// Fetch, behind the dirty-buffer gate, whose refusal names the files; a failure shows the daemon's words (git's
	// own as the cause) and a next step. The section's button is always there and says afterwards what the run did.
	interface Props {
		view: SubmodulesView;
		/** The filter box above the sections. */
		filter: string;
		/** What the panel calls each submodule (names.ts), each one's own refs and the subject of its HEAD (by `rowKey`). */
		names: ReadonlyMap<string, string>;
		rootRefs: ReadonlyMap<string, RefsResult>;
		subjects: ReadonlyMap<string, string>;
		/** Reads the section again after a failed first read. */
		onretry: () => void;
		/** The tab's Update, shared by the section's button and the rows' (plan m2n P5). */
		updater: SubmoduleUpdateModel;
		updateView: UpdateView;
	}
	let { view, filter, names, rootRefs, subjects, onretry, updater, updateView }: Props = $props();

	let open = $state(true);
	const rows = $derived(view.state === 'ready' ? submoduleRows(view.list, filter, { names, refs: rootRefs, subjects }) : []);
	const summary = $derived(updateView.summary === null ? null : summaryText(updateView.summary, (p) => names.get(p) ?? p));
	/** The state an Update button of `path` (null: the section's) carries: the model's when it is this one's, else idle. */
	const stateOf = (path: string | null) => (path === null ? (updateView.all ? updateView.state : 'idle') : !updateView.all && updateView.path === path ? updateView.state : 'idle');
	const mine = (path: string | null) => updateView.busy && (path === null ? updateView.all : !updateView.all && updateView.path === path);
	const title = (path: string | null) => {
		const what = path === null ? (view.truncated ? 'the listed submodules' : 'every submodule') : path;
		return mine(path) ? (updateView.state === 'queued' ? `The update of ${what} is queued` : `Updating ${what}`) : `Update ${what} to the recorded commit${path === null ? 's' : ''}`;
	};
</script>

{#snippet cancelButton()}
	<button
		type="button"
		class="icon"
		data-op-cancel
		disabled={updateView.cancelling}
		title={updateView.cancelling ? 'Cancelling…' : 'Cancel the update'}
		aria-label="Cancel the update"
		onclick={() => updater.cancel()}
	>
		<X size={12} />
	</button>
{/snippet}

<section class="sec" data-refs-section="submodules" data-count={rows.length}>
	<h3>
		<button type="button" class="sec-head" aria-expanded={open} onclick={() => (open = !open)}>
			{#if open}<ChevronDown size={12} />{:else}<ChevronRight size={12} />{/if}
			<span class="title">SUBMODULES</span>
			<span class="n">{rows.length}</span>
		</button>
		{#if view.state === 'ready' && view.list.length > 0}
			<button
				type="button"
				class="all"
				data-submodules-update="all"
				data-op-state={stateOf(null)}
				disabled={updateView.busy}
				title={title(null)}
				aria-label="Update every submodule"
				onclick={() => void updater.updateAll(view.list.map((m) => m.path))}
			>
				{#if mine(null)}<Spinner size={11} />{:else}<RefreshCw size={12} />{/if}
				<span>Update submodules</span>
			</button>
			{#if view.truncated}<span class="trunc" data-update-truncated title="The repository has more submodules than the list holds; only the listed ones are updated.">listed only</span>{/if}
			{#if mine(null)}{@render cancelButton()}{/if}
		{/if}
	</h3>
	{#if updateView.blocked.length > 0}
		<div class="banner" role="alert" data-dirty-block>
			<div class="what">
				<p class="msg">{updateView.blockedText}</p>
				<ul class="files" aria-label="Files with unsaved changes">
					{#each updateView.blocked as file (file)}
						<li>{file}</li>
					{/each}
				</ul>
			</div>
			<button type="button" class="icon" aria-label="Dismiss" title="Dismiss" onclick={() => updater.dismiss()}><X size={12} /></button>
		</div>
	{/if}
	{#if summary !== null}
		<div class="banner" role="status" data-update-summary>
			<div class="what">
				<p class="done">{summary}</p>
			</div>
			<button type="button" class="icon" aria-label="Dismiss" title="Dismiss" onclick={() => updater.dismiss()}><X size={12} /></button>
		</div>
	{/if}
	{#if updateView.failure}
		<div class="banner" role="alert" data-op-error data-op-of="submodules">
			<div class="what">
				<p class="msg">{updateView.failure.code ? `${updateView.failure.code}: ` : ''}{updateView.failure.message}</p>
				{#if updateView.failure.cause}<pre class="cause">{updateView.failure.cause}</pre>{/if}
				{#if updateView.failure.nextStep}<p class="next">{updateView.failure.nextStep}</p>{/if}
			</div>
			<button type="button" class="icon" aria-label="Dismiss" title="Dismiss" onclick={() => updater.dismiss()}><X size={12} /></button>
		</div>
	{/if}
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
						<span class="main">
							<span class="line">
								<span class="text name" data-submodule-name>{row.name}</span>
								<span class="state" data-state-mark>{row.state}</span>
							</span>
							<span class="line meta">
								{#if row.branch !== undefined}<span class="branch" data-submodule-branch title={row.branchTitle}>{row.branch}</span>{/if}
								<span class="oids" data-submodule-oids>{row.oids}</span>
								{#if row.subject !== undefined}<span class="text subject" data-submodule-subject title={row.subject}>{row.subject}</span>{/if}
							</span>
						</span>
						<button
							type="button"
							class="icon"
							data-submodule-update={row.path}
							data-op-state={stateOf(row.path)}
							disabled={updateView.busy}
							title={title(row.path)}
							aria-label={`Update ${row.path}`}
							onclick={() => void updater.update(row.path)}
						>
							{#if mine(row.path)}<Spinner size={11} />{:else}<RefreshCw size={12} />{/if}
						</button>
						{#if mine(row.path)}{@render cancelButton()}{/if}
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
		display: flex;
		align-items: stretch;
		margin: 0;
		font-size: var(--fs-small);
		background: var(--bg-raised);
	}
	.all {
		display: inline-flex;
		align-items: center;
		gap: var(--sp-2);
		flex: none;
		min-height: var(--row);
		border-color: transparent;
		border-radius: 0;
		background: transparent;
		font-size: var(--fs-small);
	}
	.trunc {
		align-self: center;
		flex: none;
		padding: 0 var(--sp-2);
		font-size: var(--fs-small);
		color: var(--text-3);
	}
	.icon {
		flex: none;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		min-height: 0;
		padding: 2px;
		border: 0;
		background: transparent;
		color: var(--text-3);
	}
	.row:not(:hover, :focus-within) .icon:not([data-op-cancel], [data-op-state='running'], [data-op-state='queued']) {
		opacity: 0.35;
	}
	.banner {
		display: flex;
		align-items: flex-start;
		gap: var(--sp-3);
		padding: var(--sp-2) var(--sp-4);
		border-bottom: 1px solid var(--border);
		font-size: var(--fs-small);
	}
	.banner .what {
		flex: 1;
		min-width: 0;
	}
	.banner p,
	.banner pre {
		margin: 0;
		overflow-wrap: anywhere;
		white-space: pre-wrap;
		unicode-bidi: isolate;
	}
	.banner .msg {
		color: var(--danger);
	}
	.banner .cause {
		font-family: var(--font-mono);
		color: var(--text-2);
		max-height: 8em;
		overflow: auto;
	}
	.banner .next {
		color: var(--text-2);
	}
	.banner .files {
		margin: 0;
		padding-left: var(--sp-5, 20px);
		max-height: 8em;
		overflow: auto;
		overflow-wrap: anywhere;
		unicode-bidi: isolate;
	}
	.sec-head {
		display: flex;
		align-items: center;
		gap: var(--sp-3);
		flex: 1;
		min-width: 0;
		min-height: var(--row);
		border: 0;
		border-radius: 0;
		background: transparent;
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
	.main {
		flex: 1 1 0;
		min-width: 0;
		display: flex;
		flex-direction: column;
		padding: 2px 0;
	}
	.line {
		display: flex;
		align-items: center;
		gap: var(--sp-3);
		min-width: 0;
	}
	.meta {
		font-size: var(--fs-small);
		color: var(--text-3);
	}
	.branch {
		unicode-bidi: isolate;
		flex: 0 1 auto;
		min-width: 0;
		max-width: 60%;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		color: var(--text-2);
	}
	.subject {
		flex: 1 1 0;
	}
	.oids {
		unicode-bidi: isolate;
		flex: none;
		font-family: var(--font-mono);
		font-size: var(--fs-small);
		color: var(--text-3);
	}
	.banner .done {
		color: var(--success);
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
