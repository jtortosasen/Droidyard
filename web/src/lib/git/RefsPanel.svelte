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
	import { isRemoteHead, targetLabel, type BranchTarget } from './branchops';
	import type { MenuId } from './controls';
	import Download from '@lucide/svelte/icons/download';
	import X from '@lucide/svelte/icons/x';
	import Spinner from '../projects/Spinner.svelte';
	import type { FetchModel, FetchView } from './fetch';
	import type { HistoryStore, HistoryView } from './history';
	import { loadFavourites, panelModel, saveFavourites, toggled, type RefRow, type SectionId } from './refs';
	import SubmodulesSection from './SubmodulesSection.svelte';
	import type { SubmodulesView } from './submodules';
	import type { SubmoduleUpdateModel, UpdateView } from './subupdate';

	// The Git tab's side panel, after SourceGit's (plan m2n step 6; the owner's items 4 and 7): a
	// filter box over collapsible sections with counts: LOCAL BRANCHES (the current one marked,
	// folders by `/`, starred favourites first), REMOTES, TAGS, SUBMODULES (plan m2n P4: its own
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
		/** The Submodules section's Update (plan m2n P5). */
		updater: SubmoduleUpdateModel;
		updateView: UpdateView;
		/** An enabled entry of a branch's menu was chosen. */
		onentry: (id: MenuId, target: BranchTarget) => void;
	}
	let { view, store, workspaceId, storage, fetcher, fetchView, submodules, onretrysubmodules, updater, updateView, onentry }: Props = $props();

	// The remotes that have a remote-tracking branch listed; a remote never fetched has none and
	// is not named by git.refs, so it has no button of its own (the toolbar's Fetch reaches it).
	const remoteNames = $derived([...new Set((view.refs?.remotes ?? []).map((r) => r.remote))].sort());

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
	/** The branch a local or remote-tracking row is; null for a tag. */
	function targetOf(row: RefRow): BranchTarget | null {
		const oid = row.oid === undefined ? {} : { oid: row.oid };
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
					<SubmodulesSection view={submodules} {filter} onretry={onretrysubmodules} {updater} {updateView} />
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
