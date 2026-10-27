<script lang="ts">
	import ChevronsUpDown from '@lucide/svelte/icons/chevrons-up-down';
	import FolderPlus from '@lucide/svelte/icons/folder-plus';
	import GitBranch from '@lucide/svelte/icons/git-branch';
	import X from '@lucide/svelte/icons/x';
	import { failureText, type Failure } from '../build/failure';
	import { rootKey, type ProjectEntry, type ProjectsModel, type ProjectsView } from './projects';
	import { heldByText, type SessionsModel } from './sessions';
	import SessionLine from './SessionLine.svelte';
	import Spinner from './Spinner.svelte';
	import type { TreeRow } from './trees';

	// The top of the column (ADR-0134 §1, §2): the tab's project as its trees, the project's name
	// a disclosure inside the column (a popover would hide on the pointer leaving it, ADR-0052 §3)
	// listing the recents, Open folder... and the notes. Every name, branch and title is untrusted
	// text (ADR-0015 §1): a text node.
	interface Props {
		projects: ProjectsModel;
		sessions: SessionsModel;
		view: ProjectsView | null;
		trees: readonly TreeRow[];
		menu: readonly ProjectEntry[];
		/** The tab's project name; null when no project is selected. */
		projectName: string | null;
		/** The tab's main tree root, whose menu entry is current. */
		projectRoot: string | null;
		/** The session the selection follows. */
		following: string | null;
		now: number;
		connected: boolean;
		treesError: Failure | null;
		onchosen?: () => void;
		onrelease: (workspace: string) => void;
	}
	let { projects, sessions, view, trees, menu, projectName, projectRoot, following, now, connected, treesError, onchosen, onrelease }: Props = $props();

	let menuOpen = $state(false);
	let typed = $state('');
	// With no project selected the menu is the whole page (ADR-0047 §2): open by itself.
	const expanded = $derived(projectName === null || menuOpen);

	const dotLabel = (t: TreeRow) => `${t.state}${t.trusted ? '' : ', not trusted'}`;
	// A session on a tree carries the hold it has (its line says it, with Release): the tree
	// says it only when nobody listed here does.
	const heldBySession = (workspace: string) => trees.some((x) => x.sessions.some((s) => s.row.occupies === workspace));
	// The tree's state in the leading slot: working, busy, connected and idle, open, closed.
	function stateOf(t: TreeRow, working: number): { kind: 'working' | 'busy' | 'connected' | 'open' | 'closed'; word: string } {
		if (working > 0) return { kind: 'working', word: 'working' };
		if (!t.open) return { kind: 'closed', word: 'closed' };
		if (t.state !== 'ready') return { kind: 'busy', word: 'busy' };
		if (t.sessions.length > 0) return { kind: 'connected', word: 'open, sessions connected' };
		return { kind: 'open', word: 'open' };
	}
	const nameOf = (id: string) => trees.find((t) => t.workspace === id)?.name;

	// A tree is a selection that stays where it is: it ends any follow (ADR-0134 §5).
	function chooseTree(t: TreeRow) {
		sessions.unfollow();
		if (t.workspace !== undefined) void projects.select(t.workspace);
		else void projects.openRoot(t.root);
		onchosen?.();
	}
	function chooseEntry(e: ProjectEntry) {
		sessions.unfollow();
		if (e.id !== undefined) void projects.select(e.id);
		else void projects.openRoot(e.root);
		menuOpen = false;
		onchosen?.();
	}
	function openTyped(e: SubmitEvent) {
		e.preventDefault();
		void projects.openRoot(typed);
	}
	function followSession(key: string, workspace: string | null) {
		sessions.select(key);
		sessions.follow(key);
		if (workspace !== null) {
			void projects.select(workspace);
			onchosen?.();
		}
	}
</script>

<section class="trees" aria-label="Project">
	<h2>{projectName === null ? 'Projects' : 'Worktrees'}</h2>
	<div class="head">
		<button type="button" class="project" aria-expanded={expanded} aria-label="Switch project, now {projectName ?? 'none'}" disabled={projectName === null} onclick={() => (menuOpen = !menuOpen)}>
			<span class="pname">{projectName ?? 'Choose a project'}</span>
			{#if projectName !== null}<ChevronsUpDown size={13} />{/if}
		</button>
		{#if projectRoot !== null}<span class="proot" title={projectRoot}>{projectRoot}</span>{/if}
	</div>

	{#if view?.error}
		<p class="note bad">{failureText(view.error)}</p>
	{/if}

	{#if expanded}
		<div class="menu">
			<ul class="entries">
				{#each menu as e (rootKey(e.root))}
					{@const current = projectRoot !== null && rootKey(projectRoot) === rootKey(e.root)}
					<li class="line" class:selected={current}>
						<button type="button" class="entry" aria-current={current ? 'true' : undefined} disabled={view?.opening} title={e.root} onclick={() => chooseEntry(e)}>
							<span class="dot" data-state={e.open ? 'open' : 'closed'} title={e.open ? 'Open' : 'Closed'}></span>
							<span class="ename" class:closed={!e.open}>{e.name}</span>
						</button>
						{#if e.removable && e.id !== undefined}
							{@const id = e.id}
							<button type="button" class="remove" aria-label="Remove {e.name} from the recent projects" title="Remove from the recent projects" disabled={view?.opening} onclick={() => void projects.forget(id)}>
								<X size={12} />
							</button>
						{/if}
					</li>
				{/each}
			</ul>
			{#if view && menu.length === 0}
				<p class="note">No projects yet.</p>
			{/if}
			{#if view?.truncated}
				<p class="note">Only the 50 most recent projects are listed.</p>
			{/if}
			{#if view?.chooser === 'available'}
				<button type="button" class="open" disabled={view.choosing || view.opening} onclick={() => void projects.chooseFolder()}>
					<FolderPlus size={13} /> Open folder…
				</button>
			{:else}
				<form class="typed" onsubmit={openTyped}>
					<label>
						Project root
						<input type="text" bind:value={typed} />
					</label>
					<button type="submit" disabled={typed.trim() === '' || !!view?.opening}>Open</button>
				</form>
				{#if view?.chooser === 'unavailable' && view.chooserReason}
					<p class="note">No folder dialog: {view.chooserReason}</p>
				{/if}
			{/if}
			{#if view?.choosing}
				<p class="note">Waiting for the folder dialog on the daemon's machine…</p>
			{:else if view?.opening}
				<p class="note">Opening…</p>
			{/if}
			{#if view && view.watch.state !== 'watching' && view.watch.state !== 'starting' && view.watch.state !== 'idle'}
				<p class="note">Project list: {view.watch.state} {view.watch.detail}</p>
			{/if}
		</div>
	{/if}

	{#if treesError}
		<p class="note bad">Worktrees could not be read: {failureText(treesError)}</p>
	{/if}

	<ul class="rows">
		{#each trees as t (t.root)}
			{@const working = t.sessions.filter((s) => s.row.running).length}
			{@const st = stateOf(t, working)}
			{@const nestedHolder = t.workspace !== undefined && heldBySession(t.workspace)}
			{@const held = t.holder !== undefined && t.workspace !== undefined && !nestedHolder}
			{@const showBranch = t.branch !== undefined && t.branch !== t.name}
			<li class="tree" data-tree={t.root}>
				<div class="line" class:selected={t.current}>
					<button type="button" class="pick" aria-current={t.current ? 'true' : undefined} disabled={t.gone || !!view?.opening} title={t.gone ? 'This worktree no longer exists' : t.root} onclick={() => chooseTree(t)}>
						<span class="ico" data-state={st.kind} title={dotLabel(t)}>
							{#if st.kind === 'working'}<Spinner size={11} />{:else}<span class="mark"></span>{/if}
							<span class="sr">{st.word}</span>
						</span>
						<span class="top">
							<span class="name" class:closed={!t.open}>{t.name}</span>
							{#if t.primary}<span class="primary" title="The project's main tree">primary</span>{/if}
							{#if t.sessions.length > 0}
								<span class="count" class:working={working > 0} title="{t.sessions.length} connected, {working} working" aria-label="{t.sessions.length} {t.sessions.length === 1 ? 'session' : 'sessions'}, {working} working">{t.sessions.length}</span>
							{/if}
						</span>
						<!-- A branch named as its worktree says nothing new: no second line for it alone. -->
						{#if showBranch || t.gone || (t.holder !== undefined && !nestedHolder)}
							<span class="sub">
								{#if showBranch}<span class="type" title="A linked worktree"><GitBranch size={12} /></span><span class="branch" title="Branch">{t.branch}</span>{/if}
								{#if t.gone}<span class="gone">gone</span>{/if}
								{#if t.holder && !nestedHolder}<span class="held" title="Another chat may not enter or write here">{heldByText(t.holder.label, t.holder.idle)}</span>{/if}
							</span>
						{/if}
					</button>
					{#if held && t.workspace !== undefined}
						{@const id = t.workspace}
						<button type="button" class="release" title="End the occupation; the session stays where it is" disabled={!connected} onclick={() => onrelease(id)}>Release</button>
					{/if}
					{#if t.gone && t.workspace !== undefined}
						{@const id = t.workspace}
						<button type="button" class="remove" aria-label="Remove {t.name} from the recent projects" title="Remove from the recent projects" onclick={() => void projects.forget(id)}>
							<X size={12} />
						</button>
					{/if}
				</div>
				{#if t.sessions.length > 0}
					<ul class="sessions">
						{#each t.sessions as s (s.row.key)}
							<SessionLine
								row={s.row}
								title={s.title}
								untitled={s.title === `${s.row.label} connected`}
								kind={s.client}
								followed={following === s.row.key}
								{now}
								{connected}
								releasable
								worktreeName={nameOf}
								onchoose={() => followSession(s.row.key, s.row.workspace)}
								{onrelease}
							/>
						{/each}
					</ul>
				{/if}
			</li>
		{/each}
	</ul>
</section>

<style>
	.trees {
		display: flex;
		flex-direction: column;
		gap: var(--sp-2);
		min-height: 0;
		font-size: var(--fs-base);
	}
	h2 {
		padding: var(--sp-4) var(--sp-6) 0;
	}
	.head {
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		gap: 0;
		padding: var(--sp-2) var(--sp-4) var(--sp-2) var(--sp-3);
		min-width: 0;
	}
	.proot {
		max-width: 100%;
		padding: 0 var(--sp-3);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		font-family: var(--font-mono);
		font-size: var(--fs-small);
		color: var(--text-3);
	}
	.project {
		display: inline-flex;
		align-items: center;
		gap: var(--sp-2);
		min-width: 0;
		min-height: var(--row);
		padding: 0 var(--sp-3);
		border: 0;
		background: transparent;
		color: var(--text);
		font-size: var(--fs-title);
		font-weight: 600;
	}
	.project:hover:not(:disabled) {
		background: var(--bg-hover);
	}
	.project:disabled {
		color: var(--text-2);
	}
	.pname {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.open {
		display: inline-flex;
		align-items: center;
		align-self: flex-start;
		gap: var(--sp-2);
		min-height: 0;
		height: calc(var(--row) - 2px);
		margin: 0 var(--sp-4);
		padding: 0 var(--sp-4);
		font-size: var(--fs-small);
	}
	.menu {
		display: flex;
		flex-direction: column;
		gap: var(--sp-2);
		margin: 0 var(--sp-3) var(--sp-3);
		padding: var(--sp-2) 0 var(--sp-3);
		border-block: 1px solid var(--border);
	}
	.entries,
	.rows,
	.sessions {
		list-style: none;
		margin: 0;
		padding: 0;
	}
	.entries {
		display: flex;
		flex-direction: column;
		gap: 1px;
	}
	.rows {
		display: flex;
		flex-direction: column;
		gap: var(--sp-3);
		padding: 0 var(--sp-3);
	}
	.note {
		margin: 0;
		padding: 0 var(--sp-4);
		color: var(--text-2);
	}
	.note.bad {
		color: var(--danger);
	}
	.typed {
		display: flex;
		align-items: flex-end;
		gap: var(--sp-3);
		padding: 0 var(--sp-4);
	}
	.typed label {
		flex: 1;
		display: flex;
		flex-direction: column;
		gap: var(--sp-2);
		font-size: var(--fs-small);
		color: var(--text-2);
	}
	.typed input {
		width: 100%;
	}
	.line {
		display: flex;
		align-items: flex-start;
		gap: var(--sp-2);
		border-radius: var(--radius-control);
	}
	.entry,
	.pick {
		flex: 1 1 0;
		min-width: 0;
		border: 0;
		border-radius: var(--radius-control);
		background: transparent;
		color: var(--text);
		font-weight: 400;
		text-align: left;
	}
	.line:not(.selected) .entry:hover:not(:disabled),
	.line:not(.selected) .pick:hover:not(:disabled) {
		background: var(--bg-hover);
	}
	.entry {
		display: flex;
		align-items: center;
		gap: var(--sp-4);
		min-height: var(--row);
		padding: 0 var(--sp-4);
	}
	.ename.closed,
	.name.closed {
		color: var(--text-2);
	}
	.pick {
		display: grid;
		grid-template-columns: 14px minmax(0, 1fr);
		align-items: center;
		gap: 1px var(--sp-4);
		min-height: calc(var(--row) + var(--sp-3));
		padding: var(--sp-3) var(--sp-4);
	}
	.ico {
		display: inline-grid;
		place-items: center;
		height: calc(var(--fs-base) * var(--lh-base));
	}
	.mark {
		box-sizing: border-box;
		width: 8px;
		height: 8px;
		border-radius: 50%;
	}
	.ico[data-state='connected'] .mark {
		background: var(--success);
	}
	.ico[data-state='open'] .mark {
		border: 1.5px solid var(--text-3);
	}
	.ico[data-state='closed'] .mark {
		background: var(--text-3);
	}
	.ico[data-state='busy'] .mark {
		background: var(--warning);
	}
	.type {
		display: inline-flex;
		flex: none;
		color: var(--text-2);
	}
	.top {
		display: flex;
		align-items: center;
		gap: var(--sp-3);
		min-width: 0;
	}
	.name {
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		font-weight: 500;
	}
	.primary {
		flex: none;
		padding: 0 var(--sp-3);
		border: 1px solid var(--border);
		border-radius: 3px;
		color: var(--text-2);
		font-size: var(--fs-label);
		line-height: var(--lh-base);
	}
	.count {
		flex: none;
		min-width: 16px;
		padding: 0 var(--sp-3);
		border-radius: 8px;
		background: var(--bg-active);
		color: var(--text-2);
		font-size: var(--fs-label);
		text-align: center;
	}
	.count.working {
		background: var(--accent);
		color: var(--accent-text);
	}
	.dot {
		flex: none;
		width: 7px;
		height: 7px;
		border-radius: 50%;
	}
	.dot[data-state='open'] {
		background: var(--success);
	}
	.dot[data-state='closed'] {
		background: var(--text-3);
	}
	.sub {
		grid-column: 2;
		display: flex;
		align-items: center;
		gap: var(--sp-3);
		min-width: 0;
		font-family: var(--font-mono);
		font-size: var(--fs-small);
		color: var(--text-2);
	}
	.branch,
	.held {
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.gone {
		flex: none;
		color: var(--warning);
	}
	.sessions {
		margin: var(--sp-1) 0 var(--sp-2) var(--sp-6);
		padding-left: var(--sp-3);
		border-left: 1px solid var(--border);
	}
	.remove {
		flex: none;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		min-height: 0;
		width: var(--row);
		height: var(--row);
		padding: 0;
		border: 0;
		background: transparent;
		color: var(--text-3);
	}
	.release {
		flex: none;
		min-height: 0;
		height: calc(var(--row) - 2px);
		margin-top: var(--sp-3);
		padding: 0 var(--sp-4);
		font-size: var(--fs-small);
	}
</style>
