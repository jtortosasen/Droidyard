<script lang="ts">
	import { untrack } from 'svelte';
	import GitBranch from '@lucide/svelte/icons/git-branch';
	import { groupedRows, MARK_WORDS, truncatedNote } from './changes';
	import type { GitView, GitWatch } from './watch';

	// The Git side of the left panel (plan m2l step 5; ADR-0067 §5): the paths that differ from
	// HEAD, grouped by folder, each with its letter. A file row opens the file as the Editor's
	// preview tab and shows the Diff tab (the parent does that). Names are text nodes, never
	// markup. The watch ends while the page is hidden. Step 8 adds the Tree view and the flat list.
	interface Props {
		watch: GitWatch;
		/** A changed file was chosen. */
		onopen: (path: string) => void;
	}
	let { watch, onopen }: Props = $props();

	let view = $state.raw<GitView>(untrack(() => watch.view));
	let selected = $state<string | null>(null);

	$effect(() => {
		const w = watch;
		view = w.view;
		return w.subscribe(() => {
			view = w.view;
		});
	});

	const rows = $derived(groupedRows(view.entries));
	const note = $derived(truncatedNote(view.truncated, view.entries.length));
	const refused = $derived(view.state === 'error' || view.state === 'ended');
	const branch = $derived(view.head === null ? '' : (view.head.branch ?? (view.head.oid ? `detached at ${view.head.oid.slice(0, 7)}` : 'no commit yet')));

	function choose(path: string) {
		selected = path;
		onopen(path);
	}
</script>

<svelte:document onvisibilitychange={() => watch.setHidden(document.hidden)} />

<div class="git">
	{#if branch !== ''}
		<header>
			<GitBranch size={13} />
			<span class="branch" title={branch}>{branch}</span>
		</header>
	{/if}
	{#if refused && view.problem}
		<div class="problem" role="alert">
			<p>{view.problem.text}</p>
			<button type="button" onclick={() => watch.retry()}>Retry</button>
		</div>
	{:else if view.head === null}
		<p class="note" role="status">{view.state === 'paused' ? 'Paused while the page is hidden.' : 'Reading the status…'}</p>
	{:else}
		{#if view.state === 'waiting' && view.problem}
			<p class="note" role="status">{view.problem.text}; trying again.</p>
		{/if}
		{#if rows.length === 0}
			<p class="note" role="status">No changes against HEAD.</p>
		{:else}
			<div class="rows" role="list" aria-label="Changed files">
				{#each rows as r (r.key)}
					{#if r.kind === 'dir'}
						<div class="dir" role="listitem" style:padding-left="calc(var(--sp-4) + {r.depth - 1} * var(--sp-5))" title={r.path}>
							<span class="label">{r.label}</span>
						</div>
					{:else}
						<div role="listitem">
							<button
								type="button"
								class="file"
								class:selected={selected === r.path}
								class:deleted={r.deleted}
								style:padding-left="calc(var(--sp-4) + {r.depth - 1} * var(--sp-5))"
								title={r.origPath === undefined ? r.path : `${r.origPath} → ${r.path}`}
								aria-label="{r.label}, {MARK_WORDS[r.mark]}"
								onclick={() => choose(r.path)}
							>
								<span class="label">{r.label}</span>
								{#if r.origPath !== undefined}<span class="orig">{r.origPath}</span>{/if}
								<span class="letter {r.mark}" aria-hidden="true">{r.letter}</span>
							</button>
						</div>
					{/if}
				{/each}
			</div>
		{/if}
		{#if note}<p class="note">{note}</p>{/if}
	{/if}
</div>

<style>
	.git {
		display: flex;
		flex-direction: column;
		height: 100%;
		min-height: 0;
		overflow-y: auto;
	}
	header {
		flex: none;
		display: flex;
		align-items: center;
		gap: var(--sp-3);
		padding: var(--sp-3) var(--sp-4);
		border-bottom: 1px solid var(--border);
		color: var(--text-2);
	}
	.branch {
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.note {
		margin: 0;
		padding: var(--sp-3) var(--sp-4);
		font-size: var(--fs-small);
		color: var(--text-2);
	}
	.problem {
		padding: var(--sp-3) var(--sp-4);
		color: var(--danger);
	}
	.problem p {
		margin: 0 0 var(--sp-3);
		overflow-wrap: anywhere;
	}
	.dir,
	.file {
		display: flex;
		align-items: center;
		gap: var(--sp-3);
		width: 100%;
		min-height: var(--row);
		padding-right: var(--sp-4);
		color: var(--text-2);
		text-align: left;
	}
	.dir .label {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.file {
		border: 0;
		border-radius: 0;
		background: transparent;
		color: var(--text);
	}
	.file:hover {
		background: var(--bg-hover);
	}
	.file.selected {
		background: var(--accent-soft);
	}
	.file .label {
		flex: 0 1 auto;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.file.deleted .label {
		text-decoration: line-through;
		color: var(--text-2);
	}
	.orig {
		flex: 1 1 0;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		color: var(--text-3);
		font-size: var(--fs-small);
	}
	.letter {
		flex: none;
		margin-left: auto;
		min-width: 1.2em;
		text-align: center;
		font-family: var(--font-mono);
		font-size: var(--fs-small);
		color: var(--text-2);
	}
	.letter.modified {
		color: var(--warning);
	}
	.letter.added,
	.letter.untracked {
		color: var(--success);
	}
	.letter.deleted,
	.letter.conflicted {
		color: var(--danger);
	}
	.letter.renamed {
		color: var(--accent);
	}
</style>
