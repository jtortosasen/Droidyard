<script lang="ts">
	import { untrack } from 'svelte';
	import GitBranch from '@lucide/svelte/icons/git-branch';
	import ChevronDown from '@lucide/svelte/icons/chevron-down';
	import ChevronRight from '@lucide/svelte/icons/chevron-right';
	import { browserStorage } from '../ui/layout';
	import { changeGroups, MARK_WORDS, submodulesUnreadNote, truncatedNote } from './changes';
	import { shortOid } from './submodules';
	import { loadPrefs, savePrefs, type GitGroup, type GitTreeModel, type GitTreeView, type TreeRow } from './tree';
	import type { GitView, GitWatch } from './watch';

	// The Git side of the left panel (plan m2l step 5; ADR-0067 §5): the paths that differ from
	// HEAD, grouped by folder, each with its letter. A file row opens the file as the Editor's
	// preview tab and shows the Diff tab (the parent does that). Names are text nodes, never
	// markup. The watch ends while the page is hidden. Step 8: the Tree | Changes selector and the
	// Group toggle (grouped | flat), remembered per browser and project; the Tree view is `tree`'s
	// rows, whose open folders are the Git tree's own.
	interface Props {
		watch: GitWatch;
		/** The Tree view's model, a reader of the file watch (made where the watch is). */
		tree: GitTreeModel;
		workspaceId: string;
		/** A changed file was chosen. */
		onopen: (path: string) => void;
	}
	let { watch, tree, workspaceId, onopen }: Props = $props();

	const storage = browserStorage(window);
	const stored = untrack(() => loadPrefs(storage, workspaceId));
	let mode = $state<GitTreeView>(stored.view);
	let group = $state<GitGroup>(stored.group);
	const remember = () => savePrefs(storage, workspaceId, { view: mode, group });
	const pick = (m: GitTreeView) => {
		mode = m;
		remember();
	};
	const regroup = (g: GitGroup) => {
		group = g;
		remember();
	};

	let treeRows = $state.raw<readonly TreeRow[]>(untrack(() => tree.rows));
	$effect(() => {
		const t = tree;
		treeRows = t.rows;
		return t.subscribe(() => {
			treeRows = t.rows;
		});
	});

	let view = $state.raw<GitView>(untrack(() => watch.view));
	let selected = $state<string | null>(null);

	$effect(() => {
		const w = watch;
		view = w.view;
		return w.subscribe(() => {
			view = w.view;
		});
	});

	// The status feeds the Tree view's marks and its first expansion.
	$effect(() => {
		tree.setStatus(view.entries);
	});

	const parts = $derived(changeGroups(view.entries, group));
	const note = $derived(truncatedNote(view.truncated, view.entries.length));
	const unreadNote = $derived(submodulesUnreadNote(view.submodulesUnread));
	const refused = $derived(view.state === 'error' || view.state === 'ended');
	const branch = $derived(view.head === null ? '' : (view.head.branch ?? (view.head.oid ? `detached at ${view.head.oid.slice(0, 7)}` : 'no commit yet')));

	function choose(path: string) {
		selected = path;
		onopen(path);
	}
</script>

<svelte:document onvisibilitychange={() => watch.setHidden(document.hidden)} />

<div class="git">
	<div class="bar">
		<div class="seg" role="group" aria-label="Git view" data-git-view>
			{#each ['tree', 'changes'] as const as m (m)}
				<button type="button" data-view={m} aria-pressed={mode === m} onclick={() => pick(m)}>{m === 'tree' ? 'Tree' : 'Changes'}</button>
			{/each}
		</div>
		{#if mode === 'changes'}
			<div class="seg" role="group" aria-label="Group changes" data-git-group>
				{#each ['grouped', 'flat'] as const as g (g)}
					<button type="button" data-group={g} aria-pressed={group === g} onclick={() => regroup(g)}>{g === 'grouped' ? 'Grouped' : 'Flat'}</button>
				{/each}
			</div>
		{/if}
	</div>
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
		{#if mode === 'tree'}
			<div class="rows" role="list" aria-label="Project files" data-git-rows="tree">
				{#each treeRows as r (r.key)}
					{#if r.kind === 'dir'}
						<div role="listitem">
							<button
								type="button"
								class="dir"
								class:changed={r.changed}
								data-row="dir"
								data-path={r.path}
								data-expanded={r.expanded}
								aria-expanded={r.expanded}
								style:padding-left="calc(var(--sp-4) + {r.depth - 1} * var(--sp-5))"
								title={r.path}
								onclick={() => tree.toggle(r.path)}
							>
								{#if r.expanded}<ChevronDown size={13} />{:else}<ChevronRight size={13} />{/if}
								<span class="label">{r.label}</span>
								{#if r.changed}<span class="dot" aria-label="holds changes"></span>{/if}
							</button>
						</div>
					{:else}
						<div role="listitem">
							<button
								type="button"
								class="file"
								class:selected={selected === r.path}
								class:deleted={r.deleted}
								data-row="file"
								data-path={r.path}
								data-changed={r.changed}
								style:padding-left="calc(var(--sp-4) + {r.depth - 1} * var(--sp-5) + 13px + var(--sp-3))"
								title={r.path}
								aria-label={r.mark ? `${r.label}, ${MARK_WORDS[r.mark]}` : r.label}
								onclick={() => choose(r.path)}
							>
								<span class="label">{r.label}</span>
								{#if r.letter && r.mark}<span class="letter {r.mark}" aria-hidden="true">{r.letter}</span>{/if}
							</button>
						</div>
					{/if}
				{/each}
			</div>
		{:else if view.entries.length === 0}
			<p class="note" role="status">No changes against HEAD.</p>
		{:else}
			<div class="rows" role="list" aria-label="Changed files" data-git-rows={group}>
				{#each parts.groups as g (g.key)}
					{#if parts.headers}
						<div class="dir group" role="listitem" data-row="group" data-path={g.path} title={g.path === '' ? 'The project' : g.path}>
							<span class="label">{g.path === '' ? 'Project' : g.path}</span>
							{#if g.words !== ''}<span class="orig">{g.words}</span>{/if}
							{#if g.checkedOut !== undefined}<span class="oid">{shortOid(g.checkedOut)}</span>{/if}
						</div>
					{/if}
					{#each g.rows as r (r.key)}
						{#if r.kind === 'dir'}
							<div class="dir" role="listitem" data-row="dir" data-path={r.path} style:padding-left="calc(var(--sp-4) + {r.depth - 1} * var(--sp-5))" title={r.path}>
								<span class="label">{r.label}</span>
							</div>
						{:else}
							<div role="listitem">
								<button
									type="button"
									class="file"
									class:selected={selected === r.path}
									class:deleted={r.deleted}
									data-row="file"
									data-path={r.path}
									style:padding-left="calc(var(--sp-4) + {r.depth - 1} * var(--sp-5))"
									title={r.origPath === undefined ? r.path : `${r.origPath} → ${r.path}`}
									aria-label="{r.label}, {MARK_WORDS[r.mark]}"
									onclick={() => choose(r.path)}
								>
									<span class="label">{r.label}</span>
									{#if r.dir !== ''}<span class="orig">{r.dir}</span>{/if}
									{#if r.origPath !== undefined}<span class="orig">{r.origPath}</span>{/if}
									<span class="letter {r.mark}" aria-hidden="true">{r.letter}</span>
								</button>
							</div>
						{/if}
					{/each}
				{/each}
			</div>
		{/if}
		{#if note}<p class="note">{note}</p>{/if}
		{#if unreadNote}<p class="note" role="status">{unreadNote}</p>{/if}
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
	.bar {
		flex: none;
		display: flex;
		flex-wrap: wrap;
		gap: var(--sp-3);
		padding: var(--sp-3) var(--sp-4);
		border-bottom: 1px solid var(--border);
	}
	.seg {
		display: flex;
	}
	.seg button {
		font: inherit;
		font-size: var(--fs-small);
		padding: 0 var(--sp-4);
		min-height: var(--row);
		color: var(--text-2);
		background: var(--bg-panel);
		border: 1px solid var(--border);
		cursor: pointer;
	}
	.seg button:first-child {
		border-radius: var(--radius-control) 0 0 var(--radius-control);
	}
	.seg button:last-child {
		border-radius: 0 var(--radius-control) var(--radius-control) 0;
		border-left: 0;
	}
	.seg button[aria-pressed='true'] {
		color: var(--text);
		background: var(--bg-active);
	}
	button.dir {
		border: 0;
		border-radius: 0;
		background: transparent;
		cursor: pointer;
	}
	button.dir:hover {
		background: var(--bg-hover);
	}
	button.dir.changed {
		color: var(--text);
	}
	.dot {
		flex: none;
		margin-left: auto;
		width: 6px;
		height: 6px;
		border-radius: 50%;
		background: var(--warning);
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
		font-size: var(--fs-base);
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
	.group {
		padding-left: var(--sp-4);
		color: var(--text);
	}
	.oid {
		flex: none;
		margin-left: auto;
		font-family: var(--font-mono);
		font-size: var(--fs-small);
		color: var(--text-3);
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
