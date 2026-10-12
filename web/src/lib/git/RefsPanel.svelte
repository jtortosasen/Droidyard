<script lang="ts">
	import { tick, untrack } from 'svelte';
	import Check from '@lucide/svelte/icons/check';
	import ChevronDown from '@lucide/svelte/icons/chevron-down';
	import ChevronRight from '@lucide/svelte/icons/chevron-right';
	import Ellipsis from '@lucide/svelte/icons/ellipsis';
	import Folder from '@lucide/svelte/icons/folder';
	import FolderOpen from '@lucide/svelte/icons/folder-open';
	import GitBranch from '@lucide/svelte/icons/git-branch';
	import Star from '@lucide/svelte/icons/star';
	import Tag from '@lucide/svelte/icons/tag';
	import type { KeyValueStorage } from '../ui/layout';
	import BranchMenu from './BranchMenu.svelte';
	import type { HistoryStore, HistoryView } from './history';
	import { loadFavourites, panelModel, saveFavourites, toggled, type RefRow, type SectionId } from './refs';

	// The Git tab's side panel, after SourceGit's (plan m2n step 6; the owner's items 4 and 7): a
	// filter box over collapsible sections with counts: LOCAL BRANCHES (the current one marked,
	// folders by `/`, starred favourites first), REMOTES, TAGS and WORKTREES. A click on a ref
	// selects its commit in the table (the store loads at most five more pages to find it, else the
	// table says it is not loaded). A local branch has a menu (right click, or the ⋯ button) with
	// IntelliJ's eight entries, every one disabled until the part that enables it. Favourites are
	// remembered per browser and project. Every name is git's text, in an isolated text node.
	interface Props {
		view: HistoryView;
		store: HistoryStore;
		workspaceId: string;
		storage: KeyValueStorage | null;
	}
	let { view, store, workspaceId, storage }: Props = $props();

	let filter = $state('');
	let favourites = $state.raw<ReadonlySet<string>>(untrack(() => loadFavourites(storage, workspaceId)));
	let closedSections = $state.raw<ReadonlySet<SectionId>>(new Set());
	let closedFolders = $state.raw<ReadonlySet<string>>(new Set());
	let list = $state<HTMLDivElement>();

	const sections = $derived(view.refs === null ? null : panelModel(view.refs, { filter, favourites, collapsed: closedFolders }));

	function toggleSection(id: SectionId) {
		const next = new Set(closedSections);
		if (!next.delete(id)) next.add(id);
		closedSections = next;
	}
	function toggleFolder(key: string) {
		const next = new Set(closedFolders);
		if (!next.delete(key)) next.add(key);
		closedFolders = next;
	}
	function star(name: string) {
		favourites = toggled(favourites, name);
		saveFavourites(storage, workspaceId, favourites);
	}
	function pick(row: RefRow) {
		void store.selectRef(row.ref, row.oid);
	}

	// A ref picked from outside (Navigate to HEAD): its section and folders open, and its row
	// comes into view.
	$effect(() => {
		const ref = view.selectedRef;
		if (ref === null) return;
		untrack(() => {
			const prefix = 'refs/heads/';
			if (ref.startsWith(prefix)) {
				const parts = ref.slice(prefix.length).split('/');
				parts.pop();
				const folders = new Set(closedFolders);
				let path = '';
				for (const p of parts) {
					path = path === '' ? p : `${path}/${p}`;
					folders.delete(`local:${path}`);
				}
				if (folders.size !== closedFolders.size) closedFolders = folders;
				if (closedSections.has('local')) {
					const next = new Set(closedSections);
					next.delete('local');
					closedSections = next;
				}
			}
		});
		void tick().then(() => {
			for (const el of list?.querySelectorAll<HTMLElement>('[data-ref]') ?? []) {
				if (el.dataset.ref === ref) {
					el.scrollIntoView({ block: 'nearest' });
					break;
				}
			}
		});
	});

	// --- the branch menu --------------------------------------------------------------------
	let menuFor = $state<string | null>(null);
	let menuAt = $state({ top: 0, left: 0 });
	let pop = $state<HTMLDivElement>();

	function openMenu(name: string, x: number, y: number) {
		menuFor = name;
		menuAt = { top: Math.min(y, window.innerHeight - 260), left: Math.min(x, window.innerWidth - 220) };
		void tick().then(() => pop?.showPopover());
	}
	function menuFromButton(e: MouseEvent, name: string) {
		e.stopPropagation();
		const r = (e.currentTarget as HTMLElement).getBoundingClientRect();
		openMenu(name, r.left, r.bottom + 2);
	}
</script>

<div class="refs" data-refs>
	<div class="filter">
		<input type="search" bind:value={filter} placeholder="Filter Branches/Tags" aria-label="Filter branches and tags" />
	</div>
	<div class="lists" bind:this={list}>
		{#if sections === null}
			{#if view.refsState === 'error'}
				<div class="problem" role="alert">
					<p>{view.refsProblem}</p>
					<button type="button" onclick={() => store.refreshRefs()}>Retry</button>
				</div>
			{:else}
				<p class="note" role="status">{view.refsState === 'idle' ? 'Waiting for the status…' : 'Reading the refs…'}</p>
			{/if}
		{:else}
			{#each sections as s (s.id)}
				{@const open = !closedSections.has(s.id)}
				<section class="sec" data-refs-section={s.id} data-count={s.count}>
					<h3>
						<button type="button" class="sec-head" aria-expanded={open} onclick={() => toggleSection(s.id)}>
							{#if open}<ChevronDown size={12} />{:else}<ChevronRight size={12} />{/if}
							<span class="title">{s.title}</span>
							<span class="n">{s.count}</span>
						</button>
					</h3>
					{#if open}
						<div class="rows" role="tree" aria-label={s.title}>
							{#each s.rows as row (row.key)}
								{#if row.kind === 'folder'}
									<div
										class="row folder"
										role="treeitem"
										aria-selected="false"
										aria-level={row.depth + 1}
										aria-expanded={row.expanded}
										tabindex="-1"
										style:--depth={row.depth}
										data-folder={row.key}
										onclick={() => toggleFolder(row.key)}
										onkeydown={(e) => {
											if (e.key === 'Enter' || e.key === ' ') {
												e.preventDefault();
												toggleFolder(row.key);
											}
										}}
									>
										{#if row.expanded}<FolderOpen size={13} />{:else}<Folder size={13} />{/if}
										<span class="text">{row.label}</span>
										<span class="n">{row.count}</span>
									</div>
								{:else if row.kind === 'ref'}
									<div
										class="row"
										class:selected={view.selectedRef === row.ref}
										class:current={row.current}
										role="treeitem"
										aria-selected={view.selectedRef === row.ref}
										aria-level={row.depth + 1}
										tabindex="-1"
										style:--depth={row.depth}
										data-ref={row.ref}
										data-oid={row.oid}
										data-current={row.current ? '' : undefined}
										data-starred={row.starred ? '' : undefined}
										title={row.upstream === undefined ? row.name : `${row.name} → ${row.upstream}${row.upstreamGone ? ' (gone)' : ''}`}
										onclick={() => pick(row)}
										oncontextmenu={s.id === 'local'
											? (e) => {
													e.preventDefault();
													openMenu(row.name, e.clientX, e.clientY);
												}
											: undefined}
										onkeydown={(e) => {
											if (e.key === 'Enter') pick(row);
										}}
									>
										{#if s.id === 'tags'}<Tag size={13} />{:else}<GitBranch size={13} />{/if}
										<span class="text">{row.label}</span>
										{#if row.current}<span class="mark" title="Current branch" data-current-mark><Check size={13} /></span>{/if}
										{#if s.id === 'local'}
											<button
												type="button"
												class="icon star"
												class:on={row.starred}
												aria-pressed={row.starred ?? false}
												aria-label={row.starred ? `Remove ${row.name} from favourites` : `Add ${row.name} to favourites`}
												title={row.starred ? 'Remove from favourites' : 'Add to favourites'}
												data-star
												onclick={(e) => {
													e.stopPropagation();
													star(row.name);
												}}
											>
												<Star size={12} />
											</button>
											<button
												type="button"
												class="icon more"
												aria-haspopup="menu"
												aria-label={`Menu of ${row.name}`}
												title="Branch menu"
												data-branch-menu
												onclick={(e) => menuFromButton(e, row.name)}
											>
												<Ellipsis size={12} />
											</button>
										{/if}
									</div>
								{:else}
									<div
										class="row"
										class:current={row.current}
										role="treeitem"
										aria-selected="false"
										aria-level="1"
										tabindex="-1"
										data-worktree={row.path}
										data-oid={row.oid}
										title={row.path}
										onclick={() => {
											if (row.oid !== undefined) void store.reveal(row.oid);
										}}
										onkeydown={(e) => {
											if (e.key === 'Enter' && row.oid !== undefined) void store.reveal(row.oid);
										}}
									>
										<Folder size={13} />
										<span class="text">{row.label}</span>
										{#if row.branch !== undefined}<span class="sub">{row.branch}</span>{/if}
										{#if row.current}<span class="mark" title="This work tree"><Check size={13} /></span>{/if}
									</div>
								{/if}
							{/each}
							{#if s.rows.length === 0}
								<p class="note">{filter.trim() === '' ? 'None' : 'No match'}</p>
							{/if}
						</div>
					{/if}
				</section>
			{/each}
			{#if view.refs?.branchesTruncated || view.refs?.remotesTruncated || view.refs?.tagsTruncated || view.refs?.worktreesTruncated}
				<p class="note" role="status">Some refs are not listed (the repository has more than the daemon returns).</p>
			{/if}
		{/if}
	</div>

	<div
		bind:this={pop}
		popover="auto"
		class="pop"
		role="menu"
		aria-label="Branch menu"
		style:top="{menuAt.top}px"
		style:left="{menuAt.left}px"
		ontoggle={(e) => {
			if ((e as ToggleEvent).newState === 'closed') menuFor = null;
		}}
	>
		{#if menuFor !== null}
			<BranchMenu name={menuFor} onchoose={() => pop?.hidePopover()} />
		{/if}
	</div>
</div>

<style>
	.refs {
		flex: 1;
		min-width: 0;
		min-height: 0;
		display: flex;
		flex-direction: column;
		font-size: var(--fs-base);
	}
	.filter {
		flex: none;
		padding: var(--sp-3);
		border-bottom: 1px solid var(--border);
	}
	.filter input {
		width: 100%;
	}
	.lists {
		flex: 1;
		min-height: 0;
		overflow-y: auto;
		overscroll-behavior: contain;
	}
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
		padding: 0 var(--sp-3) 0 calc(var(--sp-4) + var(--depth, 0) * 12px);
		color: var(--text);
		cursor: default;
	}
	.row:hover {
		background: var(--bg-hover);
	}
	.row.selected {
		background: var(--accent-soft);
	}
	.row.current .text {
		font-weight: 600;
	}
	.folder {
		color: var(--text-2);
	}
	.text {
		unicode-bidi: isolate;
		flex: 0 1 auto;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.sub {
		unicode-bidi: isolate;
		flex: 1 1 0;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		color: var(--text-3);
		font-size: var(--fs-small);
	}
	.mark {
		display: inline-flex;
		color: var(--success);
	}
	.row > :global(svg:first-child) {
		flex: none;
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
	.row .text + .icon,
	.row .mark + .icon {
		margin-left: auto;
	}
	.icon.more {
		margin-left: 0 !important;
	}
	.row:not(:hover, :focus-within) .icon:not(.on) {
		opacity: 0.35;
	}
	.icon.on {
		color: var(--warning);
	}
	.icon.on :global(svg) {
		fill: currentColor;
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
	.pop {
		position: fixed;
		inset: auto;
		margin: 0;
		min-width: 200px;
		padding: var(--sp-3);
		border: 1px solid var(--border);
		border-radius: var(--radius-panel);
		background: var(--overlay-solid);
		color: var(--text);
		box-shadow: var(--shadow);
	}
	.pop:not(:popover-open) {
		display: none;
	}
	.pop:popover-open {
		display: flex;
		flex-direction: column;
		align-items: stretch;
		gap: var(--sp-1);
	}
</style>
