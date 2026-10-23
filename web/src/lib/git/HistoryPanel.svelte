<script lang="ts">
	import { untrack } from 'svelte';
	import ChevronDown from '@lucide/svelte/icons/chevron-down';
	import ChevronUp from '@lucide/svelte/icons/chevron-up';
	import Search from '@lucide/svelte/icons/search';
	import SlidersHorizontal from '@lucide/svelte/icons/sliders-horizontal';
	import Tag from '@lucide/svelte/icons/tag';
	import X from '@lucide/svelte/icons/x';
	import Splitter from '../ui/Splitter.svelte';
	import { browserStorage } from '../ui/layout';
	import { COL, NODE_R, ROW, graphWidth, laneColor, laneX, strokes } from './graph';
	import type { BranchModel, BranchView } from './branch';
	import { callRoots, checkoutOffer, compareOpening, entryAction, headSource, newBranchOffer, type BranchTarget, type NewBranchSource } from './branchops';
	import type { MenuId } from './controls';
	import CheckoutDialog from './CheckoutDialog.svelte';
	import CommitView from './CommitView.svelte';
	import { type CompareModel, type CompareView as CompareViewState } from './compare';
	import CompareView from './CompareView.svelte';
	import CreateBranchDialog from './CreateBranchDialog.svelte';
	import type { CommitFile } from './diffsource';
	import type { FetchModel, FetchView } from './fetch';
	import GitToolbar from './GitToolbar.svelte';
	import type { PullModel, PullView } from './pull';
	import { avatarHue, initials, relativeTime, type HistoryStore, type HistoryView } from './history';
	import { badgesByOid, type Badge } from './refs';
	import RefsPanel from './RefsPanel.svelte';
	import { rowAt, rowTop, rowWindow, type Expanded } from './rows';
	import { findCommits, stepMatch } from './search';
	import { folderName, rootNames, rootTitle } from './names';
	import { rootFilterMemory } from './rootfilter';
	import { initialisedRoots, type SubmodulesModel, type SubmodulesView } from './submodules';
	import type { SubmoduleUpdateModel, UpdateView } from './subupdate';
	import type { GitWatch } from './watch';

	// The bottom Git panel's History tab (plan m2n steps 4 and 6; ADR-0070 §1, §3): the
	// operations toolbar across the top, the side panel of refs at the left, and the commits
	// newest first in a virtualised table, one inline <svg> per row for the graph. Columns: GRAPH
	// & SUBJECT (the ref badges before the subject), AUTHOR (an initials avatar coloured from the
	// e-mail: the CSP admits no remote image), SHA, COMMIT TIME (relative; the date in the title).
	// The Uncommitted Changes (n) row sits on top while the work tree differs from HEAD; HEAD's
	// node is hollow; a merge is dimmed. Above the table a search finds over the rows loaded
	// (subject, author, SHA prefix); the filter-settings button is shown disabled. Every text from
	// git (subject, name, e-mail, oid, ref) is a text node in a cell with `unicode-bidi: isolate`;
	// nothing from git becomes markup. The store keeps the rows, the refs, the pages and the
	// reload rules; this component draws the window the scroll shows. It tells the store when the
	// tab is shown (its box has a height) and when the window regains focus.
	interface Props {
		store: HistoryStore;
		/** Only for the reason when the status has not arrived (not a repository, no git). */
		watch: GitWatch;
		/** The project, for what is remembered per browser and project (the favourites). */
		workspaceId: string;
		/** A changed file of an expanded commit was picked (plan m2n step 9): the shell opens the Diff tab on it. */
		onopenfile?: (file: CommitFile) => void;
		/** The tab's Fetch (plan m2n P2), shared by the toolbar and the Remotes section. */
		fetcher: FetchModel;
		/** The tab's Checkout and Create Branch (plan m2n P3), shared by the branch menu, the toolbar and the dialog. */
		branches: BranchModel;
		/** The side panel's Submodules section (plan m2n P4); it follows the store's refs (`follow`), the shell wires it. */
		submodules: SubmodulesModel;
		/** The Submodules section's Update (plan m2n P5), shared by the section's button and each row's. */
		updater: SubmoduleUpdateModel;
		/** Compare with current (plan m2n P9): the view the branch menu's entry opens in place of the table. */
		compare?: CompareModel;
		/** The toolbar's Pull (plan m2n R3), a fast-forward of the current branch. */
		puller?: PullModel;
		/** The sessions at each worktree root (ADR-0083 §4), for the side panel's WORKTREES rows. */
		worktreeSessions?: ReadonlyMap<string, readonly string[]>;
	}
	let { store, watch, workspaceId, onopenfile, fetcher, branches, submodules, updater, compare, puller, worktreeSessions }: Props = $props();

	const storage = browserStorage(window);

	const OVERSCAN = 8;
	/** Rows from the end of the loaded history at which the next page is read. */
	const NEAR_END = 100;

	// The root filter is remembered per project (plan m2n R1): restored when the roots are loaded, written on a click.
	untrack(() => store.remember(rootFilterMemory(storage, workspaceId)));

	let view = $state.raw<HistoryView>(untrack(() => store.view));
	let problem = $state<string | null>(null);
	$effect(() => {
		const s = store;
		view = s.view;
		return s.subscribe(() => {
			view = s.view;
		});
	});
	let fetchView = $state.raw<FetchView>(untrack(() => fetcher.view));
	$effect(() => {
		const f = fetcher;
		fetchView = f.view;
		return f.subscribe(() => {
			fetchView = f.view;
		});
	});
	let pullView = $state.raw<PullView | undefined>(untrack(() => puller?.view));
	$effect(() => {
		const p = puller;
		pullView = p?.view;
		return p?.subscribe(() => {
			pullView = p.view;
		});
	});
	let branchView = $state.raw<BranchView>(untrack(() => branches.view));
	$effect(() => {
		const b = branches;
		branchView = b.view;
		return b.subscribe(() => {
			branchView = b.view;
		});
	});
	let submodulesView = $state.raw<SubmodulesView>(untrack(() => submodules.view));
	$effect(() => {
		const m = submodules;
		submodulesView = m.view;
		return m.subscribe(() => {
			submodulesView = m.view;
		});
	});
	let updateView = $state.raw<UpdateView>(untrack(() => updater.view));
	$effect(() => {
		const u = updater;
		updateView = u.view;
		return u.subscribe(() => {
			updateView = u.view;
		});
	});
	let compareView = $state.raw<CompareViewState | null>(untrack(() => compare?.view ?? null));
	$effect(() => {
		const c = compare;
		compareView = c?.view ?? null;
		return c?.subscribe(() => {
			compareView = c.view;
		});
	});
	// The Create Branch dialog: where the new branch starts while it is open.
	let creating = $state.raw<NewBranchSource | null>(null);
	// The Checkout confirmation (plan m2n P8): the branch while it is open. Only with initialised submodules; otherwise
	// a checkout is made at once.
	let checking = $state.raw<BranchTarget | null>(null);
	/** The submodules a checkout or a new branch can also be applied to (ADR-0071 §6). */
	const syncRoots = $derived(initialisedRoots(submodulesView.list));
	const createOffer = $derived(creating === null ? null : newBranchOffer(creating, syncRoots, view.rootRefs, view.refs));
	const checkoutOfferNow = $derived(checking === null ? null : checkoutOffer(checking, syncRoots, view.rootRefs, view.refs));
	// The confirmation is for a project with submodules: when the last one goes, so does the dialog.
	$effect(() => {
		if (checking !== null && syncRoots.length === 0) checking = null;
	});
	const locals = $derived(new Set((view.refs?.branches ?? []).map((b) => b.name)));

	/** An enabled entry of a branch's menu: Checkout calls at once (after the dirty-buffer gate), New branch from opens the dialog. */
	function onentry(id: MenuId, target: BranchTarget) {
		const action = entryAction(id, target);
		if (action === null) return;
		if (action.kind === 'compare') {
			const opening = compareOpening(target, view.refs?.head ?? null);
			if (compare === undefined || opening === null) return;
			branches.dismiss();
			compare.open(opening);
		} else if (action.kind === 'checkout') {
			if (syncRoots.length === 0) void branches.checkout(action.target);
			else {
				branches.dismiss();
				checking = action.target;
			}
		} else {
			branches.dismiss();
			creating = action.source;
		}
	}
	function openCreate() {
		const source = headSource(view.refs?.head ?? null);
		if (source === null) return;
		branches.dismiss();
		creating = source;
	}
	async function createBranch(name: string, checkout: boolean, sync: boolean): Promise<boolean> {
		const from = creating;
		if (from === null) return false;
		const offer = createOffer;
		// The roots of the call (ADR-0074 §3): the project when it has the branch the start is, and the submodules that have it.
		const ok = await branches.createBranch(name, checkout, from.oid, offer === null ? undefined : { roots: callRoots(offer, sync), starts: offer.starts });
		if (ok) creating = null;
		return ok;
	}
	async function checkoutIn(sync: boolean): Promise<boolean> {
		const target = checking;
		if (target === null) return false;
		const ok = await branches.checkout(target, callRoots(checkoutOfferNow, sync));
		if (ok) checking = null;
		return ok;
	}
	$effect(() => {
		const w = watch;
		problem = w.view.problem?.text ?? null;
		return w.subscribe(() => {
			problem = w.view.problem?.text ?? null;
		});
	});

	let now = $state(Date.now());
	$effect(() => {
		const id = setInterval(() => (now = Date.now()), 30_000);
		return () => clearInterval(id);
	});

	let scroller = $state<HTMLDivElement>();
	let scrollTop = $state(0);
	let viewportHeight = $state(0);

	// The Uncommitted Changes row is the workspace root's: a filter that leaves it out hides the row.
	const off = $derived(view.uncommitted > 0 && (view.filter === null || view.filter.includes('.')) ? 1 : 0);
	const multi = $derived(view.roots.length > 1);
	const count = $derived(view.rows.length + off);
	// The opened commit (step 8): its panel is a fixed height that fits the viewport, and the row
	// above it is taller by that much for rowWindow, so the rows below move down by exactly it.
	const PANEL_MAX = 280;
	const PANEL_MIN = 120;
	const panelHeight = $derived(Math.min(PANEL_MAX, Math.max(PANEL_MIN, viewportHeight - ROW)));
	const expandedRow = $derived.by((): Expanded | null => {
		const e = view.expanded;
		if (e === null) return null;
		const at = store.indexOf(e.oid);
		return at < 0 ? null : { index: at + off, extra: panelHeight };
	});
	const win = $derived(rowWindow({ scrollTop, viewportHeight, rowHeight: ROW, overscan: OVERSCAN, count, expanded: expandedRow }));
	const indices = $derived(Array.from({ length: win.end - win.start }, (_, k) => win.start + k));
	const gw = $derived(graphWidth(view.width) + 6);
	const headRow = $derived(view.headOid === '' ? undefined : view.rows.find((r) => r.key === view.headOid));

	// The tab is shown when its box has a height (the pane is display:none otherwise, and the region collapsed).
	let boxHeight = $state(0);
	$effect(() => {
		store.setVisible(boxHeight > 0);
	});

	let sideWidth = $state(220);
	const clampSide = (n: number) => Math.min(420, Math.max(140, Math.round(n)));

	const badges = $derived(view.refs === null ? new Map<string, Badge[]>() : badgesByOid(view.refs));
	// Each root's refs badge its own rows (plan m2n P7).
	const rootBadges = $derived(new Map([...view.rootRefs].map(([root, refs]) => [root, badgesByOid(refs)])));
	const badgesOf = (root: string, oid: string): Badge[] => (root === '.' ? badges : rootBadges.get(root))?.get(oid) ?? [];
	/** A commit is its root's HEAD: the workspace's by the status, a submodule's by its own refs. */
	const isHeadOf = (root: string, oid: string): boolean => (root === '.' ? oid === view.headOid : view.rootRefs.get(root)?.head.oid === oid);
	const MAX_BADGES = 4;

	// The search over the rows loaded.
	let query = $state('');
	const matches = $derived(query.trim() === '' ? [] : findCommits(view.rows.map((r) => r.commit), query));
	const selectedIndex = $derived(view.selected === null ? -1 : store.indexOf(view.selected));
	const matchPos = $derived(matches.indexOf(selectedIndex));
	function find(direction: 1 | -1) {
		const k = stepMatch(matches, selectedIndex, direction);
		if (k >= 0) store.show(view.rows[matches[k]].key);
	}

	// A row asked for (a ref clicked, Navigate to HEAD, a search hit): brought into view, centred when it is out of it.
	let scrolled = -1;
	$effect(() => {
		const s = view.scrollTo;
		const el = scroller;
		if (!s || !el || s.seq === scrolled) return;
		scrolled = s.seq;
		const at = store.indexOf(s.oid);
		if (at < 0) return;
		const top = rowTop(at + off, ROW, expandedRow);
		if (top < el.scrollTop || top + ROW > el.scrollTop + el.clientHeight) el.scrollTop = Math.max(0, top - (el.clientHeight - ROW) / 2);
	});

	// The next page, when the window nears the end of what is loaded.
	$effect(() => {
		if (view.state === 'ready' && view.more && !view.loadingMore && !view.moreProblem && win.end >= count - NEAR_END) {
			untrack(() => store.loadMore());
		}
	});

	// A reload that finished says where the scroll goes: the oid at the top of the view, else the top.
	let restored = -1;
	$effect(() => {
		const r = view.restore;
		const el = scroller;
		if (!r || !el || r.seq === restored) return;
		restored = r.seq;
		const at = r.oid === null ? -1 : store.indexOf(r.oid);
		el.scrollTop = at < 0 ? 0 : rowTop(at + off, ROW, expandedRow);
	});

	function onscroll() {
		if (!scroller) return;
		scrollTop = scroller.scrollTop;
		// The commit at the top of the view; at the very top none, so a new commit shows.
		const i = rowAt(scrollTop + ROW / 2, { rowHeight: ROW, count, expanded: expandedRow }) - off;
		store.setAnchor(scrollTop < ROW / 2 || i < 0 ? null : (view.rows[i]?.key ?? null));
	}

	// A commit just opened: its panel is brought into view when it hangs below the viewport.
	let opened: string | null = null;
	$effect(() => {
		const oid = view.expanded?.oid ?? null;
		const el = scroller;
		if (oid === opened) return;
		opened = oid;
		const ex = untrack(() => expandedRow);
		if (oid === null || !el || ex === null) return;
		const top = rowTop(ex.index, ROW, ex);
		const bottom = top + ROW + ex.extra;
		if (bottom > el.scrollTop + el.clientHeight) el.scrollTop = Math.min(top, bottom - el.clientHeight);
		else if (top < el.scrollTop) el.scrollTop = top;
	});

	function move(delta: number) {
		const rows = view.rows;
		if (rows.length === 0) return;
		const cur = view.selected === null ? -1 : store.indexOf(view.selected);
		const next = Math.min(rows.length - 1, Math.max(0, cur < 0 ? (delta > 0 ? 0 : rows.length - 1) : cur + delta));
		store.select(rows[next].key);
		const el = scroller;
		if (!el) return;
		const top = rowTop(next + off, ROW, expandedRow);
		if (top < el.scrollTop) el.scrollTop = top;
		else if (top + ROW > el.scrollTop + el.clientHeight) el.scrollTop = top + ROW - el.clientHeight;
	}

	function onkeydown(e: KeyboardEvent) {
		if (e.key === 'ArrowDown') move(1);
		else if (e.key === 'ArrowUp') move(-1);
		else return;
		e.preventDefault();
	}

	/**
	 * What the filter, the stripes and the Submodules section call a root (plan m2n R1): the workspace is `.` for the hooks
	 * and the project's folder name for the eye; a submodule is its last path segment, with parents only where two share it.
	 * Worked out over every submodule listed, so the filter and the section agree.
	 */
	const names = $derived(rootNames([...new Set([...view.roots, ...submodulesView.list.map((m) => m.path)])], folderName(store.workspaceRoot)));
	const rootLabel = (root: string): string => names.get(root) ?? root;
	const rootHint = (root: string): string => rootTitle(root, store.workspaceRoot);
	// The commit each initialised submodule has checked out is read once, for its row's subject.
	$effect(() => {
		const items = submodulesView.list.filter((m) => m.state !== 'uninitialised' && m.checkedOut !== undefined).map((m) => ({ root: m.path, oid: m.checkedOut! }));
		untrack(() => store.readSubjects(items));
	});
	const when = (t: number) => new Date(t * 1000).toLocaleString();
	const uncommittedText = $derived(`Uncommitted Changes (${view.uncommitted}${view.uncommittedTruncated ? '+' : ''})`);
</script>

<svelte:window onfocus={() => store.focused()} />

<section class="history" data-history bind:clientHeight={boxHeight}>
	<GitToolbar
		head={view.refs?.head ?? null}
		onnavigate={() => void store.navigateToHead()}
		onrun={(id) => {
			if (id === 'fetch') void fetcher.run();
			else if (id === 'pull') void puller?.run();
			else if (id === 'create-branch') openCreate();
		}}
		{fetchView}
		oncancel={() => fetcher.cancel()}
		{pullView}
		onpullcancel={() => puller?.cancel()}
		onpulldismiss={() => puller?.dismiss()}
	/>
	{#if fetchView.failure}
		<div class="fetch-failed" role="alert" data-op-error>
			<div class="what">
				<p class="msg">{fetchView.failure.code ? `${fetchView.failure.code}: ` : ''}{fetchView.failure.message}</p>
				{#if fetchView.failure.cause}<pre class="cause">{fetchView.failure.cause}</pre>{/if}
				{#if fetchView.failure.nextStep}<p class="next">{fetchView.failure.nextStep}</p>{/if}
			</div>
			<button type="button" class="icon" aria-label="Dismiss" title="Dismiss" onclick={() => fetcher.dismiss()}><X size={14} /></button>
		</div>
	{/if}
	{#if creating === null && checking === null}
		{#if branchView.blocked.length > 0}
			<div class="fetch-failed" role="alert" data-dirty-block>
				<div class="what">
					<p class="msg">{branchView.blockedText}</p>
					<ul class="files" aria-label="Files with unsaved changes">
						{#each branchView.blocked as path (path)}
							<li>{path}</li>
						{/each}
					</ul>
				</div>
				<button type="button" class="icon" aria-label="Dismiss" title="Dismiss" onclick={() => branches.dismiss()}><X size={14} /></button>
			</div>
		{/if}
		{#if branchView.failure}
			<div class="fetch-failed" role="alert" data-op-error data-op-of="branch">
				<div class="what">
					<p class="msg">{branchView.failure.code ? `${branchView.failure.code}: ` : ''}{branchView.failure.message}</p>
					{#if branchView.failure.cause}<pre class="cause">{branchView.failure.cause}</pre>{/if}
					{#if branchView.failure.nextStep}<p class="next">{branchView.failure.nextStep}</p>{/if}
				</div>
				<button type="button" class="icon" aria-label="Dismiss" title="Dismiss" onclick={() => branches.dismiss()}><X size={14} /></button>
			</div>
		{/if}
	{/if}
	<div class="body">
		<aside class="side" style:width="{sideWidth}px" aria-label="Branches, tags, submodules and work trees">
			<RefsPanel {view} {store} {workspaceId} {storage} {fetcher} {fetchView} submodules={submodulesView} {names} rootRefs={view.rootRefs} subjects={view.subjects} onretrysubmodules={() => void submodules.load()} {updater} {updateView} {onentry} {worktreeSessions} />
		</aside>
		<Splitter axis="x" sign={1} value={sideWidth} clamp={clampSide} onchange={(n) => (sideWidth = clampSide(n))} label="Resize the refs list" />
		{#if compare && compareView?.open}
			<CompareView model={compare} view={compareView} onopenfile={(f) => onopenfile?.(f)} {now} />
		{:else}
		<div class="table-area">
	{#if view.state === 'idle'}
		<p class="note" role="status">{problem ?? 'Reading the status…'}</p>
	{:else if view.state === 'loading'}
		<p class="note" role="status">Reading the history…</p>
	{:else if view.state === 'error'}
		<div class="problem" role="alert">
			<p>{view.problem}</p>
			<button type="button" onclick={() => store.reload()}>Retry</button>
		</div>
	{:else if count === 0}
		<p class="note" role="status">No commits yet.</p>
	{:else}
		<div class="find" role="search">
			<Search size={13} />
			<input
				type="search"
				bind:value={query}
				placeholder="Find in the loaded commits"
				aria-label="Find in the loaded commits"
				data-find
				onkeydown={(e) => {
					if (e.key === 'Enter') {
						find(e.shiftKey ? -1 : 1);
						e.preventDefault();
					}
				}}
			/>
			{#if query.trim() !== ''}
				<span class="found" role="status" data-found={matches.length}>{matches.length === 0 ? 'No match' : `${matchPos >= 0 ? matchPos + 1 : '–'} / ${matches.length}`}</span>
			{/if}
			<button type="button" class="icon" disabled={matches.length === 0} title="Previous match" aria-label="Previous match" data-find-prev onclick={() => find(-1)}><ChevronUp size={13} /></button>
			<button type="button" class="icon" disabled={matches.length === 0} title="Next match" aria-label="Next match" data-find-next onclick={() => find(1)}><ChevronDown size={13} /></button>
			<span class="fill"></span>
			<button type="button" class="icon" disabled title="Filter settings: not available yet" aria-label="Filter settings" data-filter-settings><SlidersHorizontal size={13} /></button>
		</div>
		{#if multi}
			<div class="roots" role="group" aria-label="Repositories in the log" data-root-filter>
				<span class="roots-label">Repositories</span>
				<button type="button" class="root-option" class:on={view.filter === null} aria-pressed={view.filter === null} data-root-option-all onclick={() => store.setFilter(null)}>All</button>
				{#each view.roots as root, k (root)}
					{@const on = view.filter === null || view.filter.includes(root)}
					<button type="button" class="root-option" class:on aria-pressed={on} style:--ri={k} style:--root-color={laneColor(k)} data-root-option={root} title={rootHint(root)} onclick={() => store.toggleRoot(root)}>
						<span class="swatch" aria-hidden="true"></span><span class="text">{rootLabel(root)}</span>
					</button>
				{/each}
			</div>
		{/if}
		{#if view.notice}
			<p class="notice" role="status" data-notice>{view.notice}</p>
		{/if}
		<div class="head" role="row">
			<span class="cell main" role="columnheader">GRAPH &amp; SUBJECT</span>
			<span class="cell" role="columnheader">AUTHOR</span>
			<span class="cell" role="columnheader">SHA</span>
			<span class="cell" role="columnheader">COMMIT TIME</span>
		</div>
		<div
			class="viewport"
			class:multi
			role="grid"
			aria-label="Commits"
			aria-rowcount={count}
			tabindex="0"
			bind:this={scroller}
			bind:clientHeight={viewportHeight}
			{onscroll}
			{onkeydown}
		>
			<div class="spacer" style:height="{win.totalHeight}px">
				<div class="rows" style:transform="translateY({win.offset}px)">
					{#each indices as i (i < off ? 'uncommitted' : view.rows[i - off].key)}
						{#if i < off}
							<div class="row uncommitted" role="row" aria-rowindex={i + 1} data-row="uncommitted">
								<div class="cell main" role="gridcell">
									<span class="graph" style:width="{gw}px">
										<svg width={graphWidth(view.width)} height={ROW} viewBox="0 0 {graphWidth(view.width)} {ROW}" aria-hidden="true">
											{#if headRow}
												<path d="M {laneX(headRow.lane.column)} {ROW / 2 + NODE_R} L {laneX(headRow.lane.column)} {ROW}" class="dash" stroke={laneColor(headRow.lane.color)} />
											{/if}
											<circle cx={laneX(headRow?.lane.column ?? 0)} cy={ROW / 2} r={NODE_R} class="dash node-hollow" stroke={headRow ? laneColor(headRow.lane.color) : 'var(--text-3)'} />
										</svg>
									</span>
									<span class="text subject" data-uncommitted={view.uncommitted}>{uncommittedText}</span>
								</div>
								<div class="cell"></div>
								<div class="cell"></div>
								<div class="cell"></div>
							</div>
						{:else}
							{@const r = view.rows[i - off]}
							{@const c = r.commit}
							{@const isHead = isHeadOf(r.root, c.oid)}
							{@const rowBadges = badgesOf(r.root, c.oid)}
							<div
								class="row"
								class:merge={c.parents.length > 1}
								class:selected={view.selected === r.key}
								role="row"
								tabindex="-1"
								aria-rowindex={i + 1}
								aria-selected={view.selected === r.key}
								data-row="commit"
								data-oid={c.oid}
								data-root={r.root}
								aria-expanded={view.expanded?.key === r.key}
								data-expanded={view.expanded?.key === r.key}
								onclick={() => store.toggleExpand(r.key)}
								onkeydown={(e) => {
									if (e.key === 'Enter') store.toggleExpand(r.key);
								}}
							>
								{#if multi}
									<!-- The root's colour, down the row's left edge: the position of the root in the log. -->
									<span class="stripe" style:--ri={view.roots.indexOf(r.root)} style:--root-color={laneColor(Math.max(0, view.roots.indexOf(r.root)))} data-root-stripe={view.roots.indexOf(r.root)} title={rootHint(r.root)} aria-hidden="true"></span>
								{/if}
								<div class="cell main" role="gridcell">
									<span class="graph" style:width="{gw}px">
										<svg width={graphWidth(view.width)} height={ROW} viewBox="0 0 {graphWidth(view.width)} {ROW}" aria-hidden="true">
											{#each strokes(r.lane) as s, k (k)}
												<path d={s.d} class="lane" stroke={laneColor(s.color)} />
											{/each}
											{#if isHead}
												<circle cx={laneX(r.lane.column)} cy={ROW / 2} r={NODE_R - 0.5} class="node-hollow" stroke={laneColor(r.lane.color)} data-head />
											{:else}
												<circle cx={laneX(r.lane.column)} cy={ROW / 2} r={NODE_R} fill={laneColor(r.lane.color)} class="node" />
											{/if}
										</svg>
									</span>
									{#each rowBadges.slice(0, MAX_BADGES) as b (b.ref)}
										{@render badge(b)}
									{/each}
									{#if rowBadges.length > MAX_BADGES}
										{@const rest = rowBadges.slice(MAX_BADGES)}
										<span class="badge more" title={rest.map((b) => b.label).join(', ')} data-badge-more={rest.length}>+{rest.length}</span>
									{/if}
									<span class="text subject" title={c.subject}>{c.subject}</span>
								</div>
								<div class="cell" role="gridcell">
									<span class="avatar" style:--hue={avatarHue(c.authorEmail)} aria-hidden="true">{initials(c.authorName, c.authorEmail)}</span>
									<span class="text" title="{c.authorName} <{c.authorEmail}>">{c.authorName}</span>
								</div>
								<div class="cell" role="gridcell"><span class="text sha" title={c.oid}>{c.oid.slice(0, 7)}</span></div>
								<div class="cell" role="gridcell"><span class="text" title={when(c.commitTime)}>{relativeTime(c.commitTime, now)}</span></div>
							</div>
							{#if view.expanded?.key === r.key}
								<CommitView
									expanded={view.expanded}
									height={panelHeight}
									onclose={() => store.collapse()}
									onparent={(p) => void store.openParent(p)}
									onretry={() => store.expand(r.key)}
									{onopenfile}
								/>
							{/if}
						{/if}
					{/each}
				</div>
			</div>
		</div>
		{#if view.loadingMore || view.moreProblem}
			<footer>
				{#if view.moreProblem}
					<span class="error" role="alert">{view.moreProblem}</span>
					<button type="button" onclick={() => store.loadMore()}>Retry</button>
				{:else}
					<span role="status">Reading more…</span>
				{/if}
			</footer>
		{/if}
	{/if}
		</div>
		{/if}
	</div>
	{#if creating !== null}
		<CreateBranchDialog source={creating} {locals} {names} model={branches} view={branchView} offer={createOffer} onsubmit={createBranch} oncancel={() => {
				creating = null;
				branches.dismiss();
			}} />
	{/if}
	{#if checking !== null && checkoutOfferNow !== null}
		<CheckoutDialog target={checking} offer={checkoutOfferNow} {names} model={branches} view={branchView} onsubmit={checkoutIn} oncancel={() => {
				checking = null;
				branches.dismiss();
			}} />
	{/if}
</section>

{#snippet badge(b: Badge)}
	<span class="badge {b.kind}" class:current={b.current} data-badge={b.kind} data-badge-ref={b.ref} title={b.label}>
		{#if b.kind === 'tag'}<Tag size={10} />{/if}
		<span class="bt">{b.label}</span>
	</span>
{/snippet}

<style>
	.history {
		flex: 1;
		min-height: 0;
		display: flex;
		flex-direction: column;
		font-size: var(--fs-base);
	}
	.body {
		flex: 1;
		min-height: 0;
		display: flex;
	}
	.side {
		flex: none;
		min-width: 0;
		display: flex;
		flex-direction: column;
		background: var(--bg-panel);
	}
	.table-area {
		flex: 1;
		min-width: 0;
		min-height: 0;
		display: flex;
		flex-direction: column;
	}
	.find {
		flex: none;
		display: flex;
		align-items: center;
		gap: var(--sp-3);
		padding: var(--sp-2) var(--sp-4);
		border-bottom: 1px solid var(--border);
		color: var(--text-3);
		font-size: var(--fs-small);
	}
	.find input {
		width: 24ch;
	}
	.found {
		color: var(--text-2);
		font-family: var(--font-mono);
	}
	.fill {
		flex: 1;
	}
	.icon {
		display: inline-flex;
		align-items: center;
		border-color: transparent;
		background: transparent;
	}
	.roots {
		flex: none;
		display: flex;
		align-items: center;
		gap: var(--sp-3);
		padding: var(--sp-2) var(--sp-4);
		border-bottom: 1px solid var(--border);
		font-size: var(--fs-small);
		color: var(--text-3);
		overflow-x: auto;
	}
	.root-option {
		display: inline-flex;
		align-items: center;
		gap: var(--sp-2);
		max-width: 26ch;
		padding: 0 var(--sp-3);
		line-height: 18px;
		color: var(--text-2);
		background: transparent;
	}
	.root-option.on {
		color: var(--text);
		background: var(--accent-soft);
	}
	.swatch,
	.stripe {
		background: var(--root-color);
	}
	.swatch {
		flex: none;
		width: 8px;
		height: 8px;
		border-radius: 50%;
	}
	.stripe {
		position: absolute;
		left: 0;
		top: 0;
		bottom: 0;
		width: var(--stripe);
	}
	.notice {
		flex: none;
		margin: 0;
		padding: var(--sp-2) var(--sp-4);
		font-size: var(--fs-small);
		color: var(--warning);
		border-bottom: 1px solid var(--border);
	}
	.badge {
		flex: none;
		display: inline-flex;
		align-items: center;
		gap: var(--sp-2);
		max-width: 22ch;
		padding: 0 var(--sp-3);
		border-radius: var(--radius-control);
		font-size: var(--fs-small);
		line-height: 16px;
		background: var(--bg-active);
		color: var(--text-2);
		border: 1px solid var(--border);
	}
	.badge.branch {
		color: var(--text);
		background: var(--accent-soft);
		border-color: transparent;
	}
	.badge.branch.current,
	.badge.head {
		font-weight: 600;
		border-color: var(--accent);
	}
	.badge.tag {
		color: var(--warning);
	}
	.bt {
		unicode-bidi: isolate;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.fetch-failed {
		flex: none;
		display: flex;
		align-items: flex-start;
		gap: var(--sp-3);
		padding: var(--sp-2) var(--sp-4);
		border-bottom: 1px solid var(--border);
		font-size: var(--fs-small);
	}
	.fetch-failed .what {
		flex: 1;
		min-width: 0;
	}
	.fetch-failed p,
	.fetch-failed pre {
		margin: 0;
		overflow-wrap: anywhere;
		white-space: pre-wrap;
		unicode-bidi: isolate;
	}
	.fetch-failed .msg {
		color: var(--danger);
	}
	.fetch-failed .cause {
		font-family: var(--font-mono);
		color: var(--text-2);
		max-height: 8em;
		overflow: auto;
	}
	.fetch-failed .next {
		color: var(--text-2);
	}
	.fetch-failed .files {
		margin: 0;
		padding-left: var(--sp-5, 20px);
		max-height: 8em;
		overflow: auto;
		overflow-wrap: anywhere;
		unicode-bidi: isolate;
	}
	.note,
	.problem {
		margin: var(--sp-4);
		color: var(--text-2);
	}
	.problem p {
		color: var(--danger);
		margin: 0 0 var(--sp-3);
	}
	.head,
	.row {
		display: grid;
		grid-template-columns: minmax(0, 1fr) 160px 70px 110px;
		align-items: center;
	}
	.head {
		flex: none;
		height: var(--row);
		font-size: var(--fs-small);
		color: var(--text-3);
		letter-spacing: 0.04em;
		border-bottom: 1px solid var(--border);
		scrollbar-gutter: stable;
		overflow: hidden;
		padding-right: 15px;
	}
	.viewport {
		/* The root stripe at a row's left edge, and the gap before the graph's first lane. */
		--stripe: 5px;
		--stripe-gap: 6px;
		flex: 1;
		min-height: 0;
		overflow-y: scroll;
		overscroll-behavior: contain;
		background: var(--bg-panel);
	}
	.spacer {
		position: relative;
		overflow: hidden;
	}
	.row {
		position: relative;
		height: 24px;
		cursor: default;
	}
	.row:hover {
		background: var(--bg-hover);
	}
	.row.selected {
		background: var(--accent-soft);
	}
	.cell {
		display: flex;
		align-items: center;
		gap: var(--sp-3);
		min-width: 0;
		padding: 0 var(--sp-3);
		overflow: hidden;
		white-space: nowrap;
	}
	.cell.main {
		padding-left: 0;
	}
	/* With more than one root the main cell clears the stripe in every row (the Uncommitted row has none and stays aligned). */
	.viewport.multi .row .cell.main {
		padding-left: calc(var(--stripe) + var(--stripe-gap));
	}
	.text {
		unicode-bidi: isolate;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.cell.main .subject {
		flex: 1 1 0;
		white-space: nowrap;
	}
	.sha {
		font-family: var(--font-mono);
		font-size: var(--fs-mono);
		color: var(--text-2);
	}
	.cell:nth-child(n + 3) .text {
		color: var(--text-2);
	}
	.merge .text,
	.merge .node {
		opacity: 0.55;
	}
	.graph {
		flex: none;
		max-width: 45%;
		overflow: hidden;
		display: block;
		height: 24px;
	}
	svg {
		display: block;
	}
	.lane {
		fill: none;
		stroke-width: 2;
	}
	.node-hollow {
		fill: var(--bg-panel);
		stroke-width: 2;
	}
	.dash {
		fill: none;
		stroke-width: 1.5;
		stroke-dasharray: 3 2;
	}
	.uncommitted .subject {
		font-style: italic;
		color: var(--text-2);
	}
	.avatar {
		flex: none;
		width: 16px;
		height: 16px;
		border-radius: 50%;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		font-size: 8px;
		font-weight: 600;
		color: var(--avatar-text);
		background: hsl(var(--hue) var(--avatar-s) var(--avatar-l));
		user-select: none;
	}
	footer {
		flex: none;
		padding: var(--sp-2) var(--sp-4);
		font-size: var(--fs-small);
		color: var(--text-2);
		display: flex;
		gap: var(--sp-4);
		align-items: center;
	}
	.error {
		color: var(--danger);
	}
</style>
