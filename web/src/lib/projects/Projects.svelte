<script lang="ts">
	import Bot from '@lucide/svelte/icons/bot';
	import Check from '@lucide/svelte/icons/check';
	import Folder from '@lucide/svelte/icons/folder';
	import FolderPlus from '@lucide/svelte/icons/folder-plus';
	import GitBranch from '@lucide/svelte/icons/git-branch';
	import X from '@lucide/svelte/icons/x';
	import { failureText } from '../build/failure';
	import { groupProjects, type ProjectsModel, type ProjectsView, type ProjectRow } from './projects';
	import { duration, heldByText, holderOf, occupiesText, type AgentRow, type SessionsModel, type SessionsView } from './sessions';
	import Spinner from './Spinner.svelte';

	// The projects and agents panel (plan m2d step 7, ADR-0047, D30): the projects this
	// installation has known, newest first, with the agent rows under the project each
	// belongs to and, under a running agent, its operations with a live duration. Every
	// field is a text node.
	interface Props {
		projects: ProjectsModel;
		sessions: SessionsModel;
		/** The workspace the tab shows; null where none is. */
		selected: string | null;
		/** A project or an agent row was chosen (the overlay closes). */
		onchosen?: () => void;
		/** The connection is open: Release needs it. */
		connected?: boolean;
	}
	let { projects, sessions, selected, onchosen, connected = true }: Props = $props();

	let view = $state.raw<ProjectsView | null>(null);
	let agents = $state.raw<SessionsView>({ rows: [], runningAgents: 0 });
	let now = $state(Date.now());
	let typed = $state('');

	$effect(() => {
		const p = projects;
		const s = sessions;
		const read = () => {
			view = p.view;
			agents = s.view;
		};
		read();
		const offP = p.subscribe(read);
		const offS = s.subscribe(read);
		// Durations tick, and a row ages out of the panel without any event.
		const timer = setInterval(() => {
			now = Date.now();
			agents = s.view;
		}, 1000);
		return () => {
			offP();
			offS();
			clearInterval(timer);
		};
	});

	const rows = $derived(view?.rows ?? []);
	// The panel by project (ADR-0083 §2): a worktree is never a top-level row.
	const grouping = $derived(groupProjects(rows, agents.rows));
	const groups = $derived(grouping.groups);
	const loose = $derived(grouping.loose);

	// A project or worktree row: a selection that stays where it is, whatever the sessions do.
	function chooseProject(p: ProjectRow) {
		sessions.unfollow();
		void projects.select(p.id);
		onchosen?.();
	}
	// A session row: the selection follows that session's moves (ADR-0083 §3).
	function chooseAgent(a: AgentRow, workspace: string | null) {
		sessions.select(a.key);
		if (a.you) sessions.unfollow();
		else sessions.follow(a.key);
		if (workspace !== null) {
			void projects.select(workspace);
			onchosen?.();
		}
	}
	function openGroup(root: string) {
		void projects.openRoot(root);
	}
	function openTyped(e: SubmitEvent) {
		e.preventDefault();
		void projects.openRoot(typed);
	}
	// The owner's release of a worktree a session holds (ADR-0094 §3): it moves no session.
	let releaseError = $state<string | null>(null);
	async function release(w: ProjectRow) {
		const f = await sessions.release({ workspace: w.id });
		releaseError = f === null ? null : failureText(f);
	}
	const worktreeName = (id: string) => rows.find((r) => r.id === id)?.name;
	const age = (startedAt: string) => duration(now - Date.parse(startedAt));
	const dotLabel = (p: ProjectRow) => `${p.state}${p.trusted ? '' : ', not trusted'}`;
</script>

{#snippet agentRow(a: AgentRow, workspace: string | null, where: string | null)}
	<li>
		<button type="button" class="agent" onclick={() => chooseAgent(a, workspace)}>
			<span class="status">
				{#if a.running}
					<Spinner size={11} />
				{:else if a.result === 'completed'}
					<span class="ok" title="Last operation succeeded"><Check size={12} /></span>
				{:else if a.result === 'failed'}
					<span class="fail" title="Last operation failed"><X size={12} /></span>
				{:else}
					<span class="idle" title="Idle"></span>
				{/if}
			</span>
			{#if !a.you}<Bot size={12} />{/if}
			<span class="label">{a.label}</span>
			{#if where !== null}<span class="where" title="Worktree">{where}</span>{/if}
			{#if occupiesText(a, worktreeName) !== ''}
				<span class="holds" title="Holds this worktree or device, so that no other chat writes or acts there">holds {occupiesText(a, worktreeName)}</span>
			{/if}
			{#if a.idle && occupiesText(a, worktreeName) !== ''}<span class="stale" title="No request for a while: another chat may take over what it holds">idle holder</span>{/if}
			{#if a.unread}<span class="unread" title="Finished since you last looked"></span>{/if}
		</button>
		{#each a.operations as o (o.id)}
			<div class="op">{o.capability} · {age(o.startedAt)}</div>
		{/each}
	</li>
{/snippet}

{#snippet remove(p: ProjectRow)}
	<!-- Only a closed project: the daemon refuses an open one (ADR-0086). -->
	{#if !p.open}
		<button
			type="button"
			class="remove"
			aria-label="Remove {p.name} from the recent projects"
			title="Remove from the recent projects"
			disabled={view?.opening}
			onclick={() => void projects.forget(p.id)}
		>
			<X size={12} />
		</button>
	{/if}
{/snippet}

<section class="projects" aria-label="Projects">
	<header>
		<h2>Projects</h2>
		{#if view?.chooser === 'available'}
			<button type="button" class="open" disabled={view.choosing || view.opening} onclick={() => void projects.chooseFolder()}>
				<FolderPlus size={13} /> Open folder…
			</button>
		{/if}
	</header>

	{#if view?.error}
		<p class="note bad">{failureText(view.error)}</p>
	{/if}
	{#if view?.chooser !== 'available'}
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

	{#if releaseError}
		<p class="note bad">Release refused: {releaseError}</p>
	{/if}

	{#if view && rows.length === 0 && loose.length === 0}
		<p class="note">No projects yet.</p>
	{/if}

	<ul class="list">
		{#each groups as g (g.key)}
			{@const agentsHere = g.sessions.filter((s) => !s.session.you)}
			{@const working = agentsHere.filter((s) => s.session.running).length}
			<li data-project={g.root}>
				{#if g.project}
					{@const p = g.project}
					<div class="line">
						<button
							type="button"
							class="project"
							class:selected={p.id === selected}
							aria-current={p.id === selected ? 'true' : undefined}
							disabled={view?.opening}
							title={p.root}
							onclick={() => chooseProject(p)}
						>
							<Folder size={14} />
							<span class="name" class:closed={!p.open}>{p.name}</span>
							{#if agentsHere.length > 0}
								<span class="count" class:working={working > 0} title="{working} working">{agentsHere.length}</span>
							{/if}
							<span class="dot" data-state={p.open ? (p.state === 'ready' ? 'ready' : 'busy') : 'closed'} title={dotLabel(p)}></span>
						</button>
						{@render remove(p)}
					</div>
				{:else}
					<!-- Its worktrees are listed and the main tree is not: a group row for it, which Open opens. -->
					<div class="project group" title={g.root}>
						<Folder size={14} />
						<span class="name closed">{g.name}</span>
						{#if agentsHere.length > 0}
							<span class="count" class:working={working > 0} title="{working} working">{agentsHere.length}</span>
						{/if}
						<button type="button" class="open" disabled={view?.opening} onclick={() => openGroup(g.root)}>Open</button>
					</div>
				{/if}
				{#if g.sessions.length > 0}
					<ul class="agents">
						{#each g.sessions as s (s.session.key)}
							{@render agentRow(s.session, s.workspace, s.session.you ? null : s.where)}
						{/each}
					</ul>
				{/if}
				{#if g.worktrees.length > 0}
					<ul class="agents worktrees">
						{#each g.worktrees as w (w.id)}
							{@const holder = holderOf(agents.rows, w.id)}
							<li class="line">
								<button
									type="button"
									class="agent worktree"
									class:selected={w.id === selected}
									aria-current={w.id === selected ? 'true' : undefined}
									disabled={view?.opening}
									title={w.root}
									onclick={() => chooseProject(w)}
								>
									<GitBranch size={12} />
									<span class="label" class:closed={!w.open}>{w.name}</span>
									{#if holder}<span class="where" title="Another chat may not enter or write here">{heldByText(holder.label, holder.idle)}</span>{/if}
									<span class="dot" data-state={w.open ? (w.state === 'ready' ? 'ready' : 'busy') : 'closed'} title={dotLabel(w)}></span>
								</button>
								{#if holder}
									<button type="button" class="release" title="End the occupation; the session stays where it is" disabled={!connected} onclick={() => void release(w)}>Release</button>
								{/if}
								{@render remove(w)}
							</li>
						{/each}
					</ul>
				{/if}
			</li>
		{/each}
		{#if loose.length > 0}
			<li>
				<div class="project group"><Folder size={14} /> <span class="name">No project</span></div>
				<ul class="agents">
					{#each loose as a (a.key)}
						{@render agentRow(a, null, null)}
					{/each}
				</ul>
			</li>
		{/if}
	</ul>
	{#if view?.truncated}
		<p class="note">Only the 50 most recent projects are listed.</p>
	{/if}
</section>

<style>
	.projects {
		display: flex;
		flex-direction: column;
		gap: var(--sp-3);
		min-height: 0;
		padding: var(--sp-4);
		font-size: var(--fs-base);
	}
	header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--sp-4);
		padding: var(--sp-2) var(--sp-3);
	}
	h2 {
		margin: 0;
		font-size: var(--fs-small);
		font-weight: 600;
		letter-spacing: 0.04em;
		text-transform: uppercase;
		color: var(--text-2);
	}
	.open {
		display: inline-flex;
		align-items: center;
		gap: var(--sp-2);
		min-height: 0;
		height: calc(var(--row) - 2px);
		padding: 0 var(--sp-4);
		font-size: var(--fs-small);
	}
	.typed {
		display: flex;
		align-items: flex-end;
		gap: var(--sp-3);
		padding: 0 var(--sp-3);
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
	.note {
		margin: 0;
		padding: 0 var(--sp-3);
		font-size: var(--fs-small);
		color: var(--text-2);
	}
	.note.bad {
		color: var(--danger);
	}
	.list,
	.agents {
		list-style: none;
		margin: 0;
		padding: 0;
	}
	.list {
		display: flex;
		flex-direction: column;
		gap: var(--sp-2);
		overflow: auto;
	}
	.line {
		display: flex;
		align-items: center;
		gap: var(--sp-2);
	}
	.line > :first-child {
		flex: 1;
		min-width: 0;
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
	.project,
	.agent {
		display: flex;
		align-items: center;
		gap: var(--sp-3);
		width: 100%;
		min-height: 0;
		height: var(--row);
		padding: 0 var(--sp-4);
		border: 0;
		border-radius: var(--radius-control);
		background: transparent;
		color: var(--text);
		text-align: left;
	}
	.project:hover:not(:disabled),
	.agent:hover {
		background: var(--bg-hover);
	}
	.project.selected {
		background: var(--accent-soft);
	}
	.project.group {
		color: var(--text-2);
		font-size: var(--fs-small);
	}
	.name,
	.label {
		flex: 1;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.name.closed {
		color: var(--text-2);
	}
	.count {
		min-width: 16px;
		padding: 0 var(--sp-3);
		border-radius: 8px;
		background: var(--bg-active);
		color: var(--text-2);
		font-size: var(--fs-small);
		text-align: center;
	}
	.count.working {
		background: var(--accent);
		color: var(--accent-text);
	}
	.dot,
	.idle,
	.unread {
		display: inline-block;
		flex: none;
		width: 7px;
		height: 7px;
		border-radius: 50%;
	}
	.dot[data-state='ready'] {
		background: var(--success);
	}
	.dot[data-state='busy'] {
		background: var(--warning);
	}
	.dot[data-state='closed'] {
		background: var(--text-3);
	}
	.agents {
		margin: 0 0 var(--sp-2) var(--sp-6);
	}
	.agent {
		color: var(--text-2);
	}
	.status {
		display: inline-grid;
		place-items: center;
		width: 14px;
		flex: none;
	}
	.idle {
		background: var(--text-3);
	}
	.ok {
		color: var(--success);
		display: inline-flex;
	}
	.fail {
		color: var(--danger);
		display: inline-flex;
	}
	.unread {
		background: var(--accent);
	}
	.where {
		flex: none;
		max-width: 40%;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		font-family: var(--font-mono);
		font-size: var(--fs-small);
		color: var(--text-3);
	}
	.holds,
	.stale {
		flex: none;
		max-width: 40%;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		font-size: var(--fs-small);
		color: var(--text-3);
	}
	.stale {
		color: var(--warning);
	}
	.release {
		flex: none;
		min-height: 0;
		height: calc(var(--row) - 2px);
		padding: 0 var(--sp-4);
		font-size: var(--fs-small);
	}
	.agent.selected {
		background: var(--accent-soft);
	}
	.label.closed {
		color: var(--text-3);
	}
	.op {
		margin-left: calc(var(--sp-4) + 14px + var(--sp-3));
		padding: 0 var(--sp-4);
		font-family: var(--font-mono);
		font-size: var(--fs-small);
		color: var(--text-3);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
</style>
