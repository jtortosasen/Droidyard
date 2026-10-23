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
	import { canCompare, isRemoteHead, targetLabel, type BranchTarget } from './branchops';
	import { branchUnion, type Holder } from './branchroots';
	import type { MenuId } from './controls';
	import Download from '@lucide/svelte/icons/download';
	import X from '@lucide/svelte/icons/x';
	import Spinner from '../projects/Spinner.svelte';
	import type { FetchModel, FetchView } from './fetch';
	import { laneColor } from './graph';
	import type { HistoryStore, HistoryView } from './history';
	import {
		NO_FOLDER_CHOICE,
		collapsedFolders,
		loadFavourites,
		localFoldersOf,
		panelModel,
		saveFavourites,
		toggled,
		toggledFolder,
		withOpened,
		type FolderChoice,
		type RefRow,
		type RefsResult,
		type SectionId,
	} from './refs';
	import SubmodulesSection from './SubmodulesSection.svelte';
	import type { SubmodulesView } from './submodules';
	import type { SubmoduleUpdateModel, UpdateView } from './subupdate';

	// The Git tab's side panel, after SourceGit's (plan m2n step 6; the owner's items 4 and 7): a
	// filter box over collapsible sections with counts: LOCAL BRANCHES (the current one marked,
	// folders by `/`, starred favourites first), REMOTES (both list the branches of every root once, each with a small
	// mark in the colour of the stripe of each root that has it when the log has several: plan m2n R2, ADR-0074 §3; one
	// that only a submodule has is listed too, and its click selects that submodule's commit), TAGS, SUBMODULES (plan m2n P4: its own
	// component and model, filtered by the same box) and WORKTREES. A click on a ref
	// selects its commit in the table (the store loads at most five more pages to find it, else the
	// table says it is not loaded). A local or a remote-tracking branch has a menu (right click, or
	// the ⋯ button) with IntelliJ's eight entries, each disabled until the part that enables it;
	// an enabled one is reported with the branch (`onentry`). Favourites are remembered per
	// browser and project. Every name is git's text, in an isolated text node.
	interface Props {
		view: HistoryView;
		store: HistoryStore;
		workspaceId: string;
		storage: KeyValueStorage | null;
		/** The tab's Fetch: each remote of the REMOTES section has a Fetch (plan m2n P2). */
		fetcher: FetchModel;
		fetchView: FetchView;
		/** The SUBMODULES section's list (plan m2n P4) and the way to read it again after a failed first read. */
		submodules: SubmodulesView;
		onretrysubmodules: () => void;
		/** What the panel calls each root (names.ts), each submodule's own refs, and the subject of each one's HEAD (plan m2n R1). */
		names: ReadonlyMap<string, string>;
		rootRefs: ReadonlyMap<string, RefsResult>;
		subjects: ReadonlyMap<string, string>;
		/** The Submodules section's Update (plan m2n P5). */
		updater: SubmoduleUpdateModel;
		updateView: UpdateView;
		/** An enabled entry of a branch's menu was chosen. */
		onentry: (id: MenuId, target: BranchTarget) => void;
		/** The sessions at each worktree root (ADR-0083 §4): the WORKTREES rows name them. */
		worktreeSessions?: ReadonlyMap<string, readonly string[]>;
	}
	let { view, store, workspaceId, storage, fetcher, fetchView, submodules, onretrysubmodules, names, rootRefs, subjects, updater, updateView, onentry, worktreeSessions }: Props = $props();

	// The remotes that have a remote-tracking branch listed; a remote never fetched has none and
	// is not named by git.refs, so it has no button of its own (the toolbar's Fetch reaches it).
	const remoteNames = $derived([...new Set((view.refs?.remotes ?? []).map((r) => r.remote))].sort());

	let filter = $state('');
	let favourites = $state.raw<ReadonlySet<string>>(untrack(() => loadFavourites(storage, workspaceId)));
	let closedSections = $state.raw<ReadonlySet<SectionId>>(new Set());
	// Branch folders start closed, except those along the current branch (plan m2n R1); the person's own opening and closing is kept.
	let folderChoice = $state.raw<FolderChoice>(NO_FOLDER_CHOICE);
	let list = $state<HTMLDivElement>();

	// The branches of every loaded root, only with more than one (the marks say which roots have a branch).
	const union = $derived(view.roots.length > 1 ? branchUnion(view.roots, view.refs, rootRefs) : undefined);
	const sections = $derived(view.refs === null ? null : panelModel(view.refs, { filter, favourites, collapsed: collapsedFolders(view.refs, folderChoice, union), ...(union !== undefined ? { union } : {}), ...(worktreeSessions !== undefined ? { worktreeSessions } : {}) }));
	const rootName = (root: string): string => names.get(root) ?? root;
	const markTitle = (h: Holder): string => (h.current ? `${rootName(h.root)}: checked out` : rootName(h.root));

	function toggleSection(id: SectionId) {
		const next = new Set(closedSections);
		if (!next.delete(id)) next.add(id);
		closedSections = next;
	}
	/** A click on a folder row, which is `open` now. */
	function toggleFolder(key: string, open: boolean) {
		folderChoice = toggledFolder(folderChoice, key, open);
	}
	function star(name: string) {
		favourites = toggled(favourites, name);
		saveFavourites(storage, workspaceId, favourites);
	}
	function pick(row: RefRow) {
		void store.selectRef(row.ref, row.oid, row.root);
	}
	/** The branch a local or remote-tracking row is; null for a tag. */
	function targetOf(row: RefRow): BranchTarget | null {
		// A branch only a submodule has: its oid is that submodule's tip (plan m2n R2).
		const oid = { ...(row.oid === undefined ? {} : { oid: row.oid }), ...(row.root === undefined ? {} : { onlyIn: row.root }) };
		if (row.remote !== undefined && row.branch !== undefined) {
			const remote: BranchTarget = { kind: 'remote', remote: row.remote, name: row.branch, ...oid };
			return isRemoteHead(remote) ? null : remote;
		}
		return row.ref.startsWith('refs/heads/') ? { kind: 'local', name: row.name, ...oid } : null;
	}

	// A ref picked from outside (Navigate to HEAD): its section and folders open, and its row
	// comes into view.
	$effect(() => {
		const ref = view.selectedRef;
		if (ref === null) return;
		untrack(() => {
			if (ref.startsWith('refs/heads/')) {
				folderChoice = withOpened(folderChoice, localFoldersOf(ref));
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
	let menuFor = $state.raw<BranchTarget | null>(null);
	let menuAt = $state({ top: 0, left: 0 });
	let pop = $state<HTMLDivElement>();

	function openMenu(target: BranchTarget, x: number, y: number) {
		menuFor = target;
		menuAt = { top: Math.min(y, window.innerHeight - 260), left: Math.min(x, window.innerWidth - 220) };
		void tick().then(() => pop?.showPopover());
	}
	function menuFromButton(e: MouseEvent, target: BranchTarget) {
		e.stopPropagation();
		const r = (e.currentTarget as HTMLElement).getBoundingClientRect();
		openMenu(target, r.left, r.bottom + 2);
	}
</script>

<div class="refs" data-refs>
	<div class="filter">
		<input type="search" bind:value={filter} placeholder="Filter Branches/Tags/Submodules" aria-label="Filter branches, tags and submodules" />
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
						{#if s.id === 'remotes' && remoteNames.length > 0}
							<div class="remote-fetch" data-remote-fetch>
								{#each remoteNames as name (name)}
									{@const mine = fetchView.busy && fetchView.remote === name}
									<span class="rf">
										<button
											type="button"
											class="rf-btn"
											data-fetch-remote={name}
											data-op-state={fetchView.remote === name ? fetchView.state : 'idle'}
											disabled={fetchView.busy}
											aria-label={`Fetch ${name}`}
											title={mine ? (fetchView.state === 'queued' ? `Fetch of ${name} is queued` : `Fetching ${name}`) : `Fetch ${name}`}
											onclick={() => void fetcher.run(name)}
										>
											{#if mine}<Spinner size={11} />{:else}<Download size={12} />{/if}
											<span class="rf-name">{name}</span>
										</button>
										{#if mine}
											<button
												type="button"
												class="rf-btn"
												data-op-cancel
												disabled={fetchView.cancelling}
												title={fetchView.cancelling ? 'Cancelling…' : `Cancel the fetch of ${name}`}
												aria-label={`Cancel the fetch of ${name}`}
												onclick={() => fetcher.cancel()}
											>
												<X size={12} />
											</button>
										{/if}
									</span>
								{/each}
							</div>
						{/if}
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
										onclick={() => toggleFolder(row.key, row.expanded)}
										onkeydown={(e) => {
											if (e.key === 'Enter' || e.key === ' ') {
												e.preventDefault();
												toggleFolder(row.key, row.expanded);
											}
										}}
									>
										{#if row.expanded}<FolderOpen size={13} />{:else}<Folder size={13} />{/if}
										<span class="text">{row.label}</span>
										<span class="n">{row.count}</span>
									</div>
								{:else if row.kind === 'ref'}
									{@const target = s.id === 'local' || s.id === 'remotes' ? targetOf(row) : null}
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
										oncontextmenu={target
											? (e) => {
													e.preventDefault();
													openMenu(target, e.clientX, e.clientY);
												}
											: undefined}
										onkeydown={(e) => {
											if (e.key === 'Enter') pick(row);
										}}
									>
										{#if s.id === 'tags'}<Tag size={13} />{:else}<GitBranch size={13} />{/if}
										<span class="text">{row.label}</span>
										{#if row.current}<span class="mark" title="Current branch" data-current-mark><Check size={13} /></span>{/if}
										{#if row.holders}
											<span class="roots" data-root-marks>
												{#each row.holders as h (h.root)}
													<span class="rm" class:cur={h.current} style:--ri={view.roots.indexOf(h.root)} style:--root-color={laneColor(Math.max(0, view.roots.indexOf(h.root)))} title={markTitle(h)} data-root-mark={h.root} data-root-current={h.current ? '' : undefined}></span>
												{/each}
											</span>
										{/if}
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
										{/if}
										{#if target}
											<button
												type="button"
												class="icon more"
												aria-haspopup="menu"
												aria-label={`Menu of ${row.name}`}
												title="Branch menu"
												data-branch-menu
												onclick={(e) => menuFromButton(e, target)}
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
										{#if row.sessions.length > 0}<span class="sub sessions" title="Sessions in this worktree">{row.sessions.join(', ')}</span>{/if}
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
				{#if s.id === 'tags'}
					<SubmodulesSection view={submodules} {filter} {names} {rootRefs} {subjects} onretry={onretrysubmodules} {updater} {updateView} />
				{/if}
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
			{@const target = menuFor}
			<BranchMenu
				name={targetLabel(target)}
				canCompare={canCompare(target, view.refs?.head ?? null)}
				compareNote={target.onlyIn === undefined ? undefined : 'Compare with current: the project does not have this branch'}
				onchoose={(id) => {
					pop?.hidePopover();
					onentry(id, target);
				}}
			/>
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
		letter-spacing: var(--tracking-label);
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
	.remote-fetch {
		display: flex;
		flex-wrap: wrap;
		gap: var(--sp-2);
		padding: var(--sp-2) var(--sp-3);
	}
	.rf {
		display: inline-flex;
		min-width: 0;
		gap: var(--sp-1);
	}
	.rf-btn {
		display: inline-flex;
		align-items: center;
		gap: var(--sp-2);
		min-width: 0;
		border-color: transparent;
		background: transparent;
		font-size: var(--fs-small);
	}
	.rf-name {
		unicode-bidi: isolate;
		min-width: 0;
		max-width: 16ch;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
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
	/* The roots that have the branch: a dot each in the colour of that root's stripe (HistoryPanel's .stripe). */
	.roots {
		flex: none;
		display: inline-flex;
		align-items: center;
		gap: 3px;
		margin-left: auto;
	}
	.rm {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		background: var(--root-color);
	}
	.rm.cur {
		box-shadow: 0 0 0 1.5px var(--bg-panel), 0 0 0 2.5px var(--text-2);
	}
	/* After the star the ⋯ follows it; alone (a remote-tracking branch) it is pushed to the end. */
	.icon.star + .icon.more {
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
		font-size: var(--fs-base);
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
