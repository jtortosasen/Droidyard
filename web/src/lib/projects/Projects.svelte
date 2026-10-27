<script lang="ts">
	import Check from '@lucide/svelte/icons/check';
	import Folder from '@lucide/svelte/icons/folder';
	import FolderPlus from '@lucide/svelte/icons/folder-plus';
	import GitBranch from '@lucide/svelte/icons/git-branch';
	import MessageSquare from '@lucide/svelte/icons/message-square';
	import X from '@lucide/svelte/icons/x';
	import { failureText } from '../build/failure';
	import { agoText, chatFolders, cut, sourceNotes, type ChatRow, type ChatsModel, type ChatsView } from './chats';
	import { projectMenu, rootKey, type ProjectsModel, type ProjectsView, type ProjectRow } from './projects';
	import { duration, heldByText, holderOf, occupiesText, type AgentRow, type SessionsModel, type SessionsView } from './sessions';
	import Spinner from './Spinner.svelte';

	// The projects panel (plan m2d step 7, ADR-0047) and, below it, the Sessions (ADR-0127):
	// the projects this installation has known, newest first, with their worktrees; then every
	// chat by client, project and the title its client shows, a live session on its chat's row
	// and, under a running one, its operations with a live duration. Every field, a chat's
	// title above all (untrusted text, ADR-0015 section 1), is a text node.
	interface Props {
		projects: ProjectsModel;
		sessions: SessionsModel;
		chats: ChatsModel;
		/** The workspace the tab shows; null where none is. */
		selected: string | null;
		/** A project or an agent row was chosen (the overlay closes). */
		onchosen?: () => void;
		/** The connection is open: Release needs it. */
		connected?: boolean;
	}
	let { projects, sessions, chats, selected, onchosen, connected = true }: Props = $props();

	let view = $state.raw<ProjectsView | null>(null);
	let agents = $state.raw<SessionsView>({ rows: [], runningAgents: 0 });
	let chatsView = $state.raw<ChatsView>({ chats: [], sources: [], truncated: false, error: null, loaded: false });
	let now = $state(Date.now());
	// "N more" opened, per client and project; in memory only.
	let opened = $state.raw<ReadonlySet<string>>(new Set());
	let copied = $state<string | null>(null);
	let shownCommand = $state<{ key: string; text: string } | null>(null);
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
		const readChats = () => (chatsView = chats.view);
		readChats();
		const offC = chats.subscribe(readChats);
		// Durations tick, and a row ages out of the panel without any event.
		const timer = setInterval(() => {
			now = Date.now();
			agents = s.view;
		}, 1000);
		return () => {
			offP();
			offS();
			offC();
			clearInterval(timer);
		};
	});

	const rows = $derived(view?.rows ?? []);
	// Step 3 keeps this view building on the new models; step 4 redraws it (ADR-0134).
	const menu = $derived(projectMenu(rows));
	const folders = $derived(chatFolders(chatsView.chats, agents.rows, rows, null));
	const sessionsIn = (root: string) =>
		agents.rows.filter((a) => {
			const r = rows.find((x) => x.id === a.workspace);
			return r !== undefined && rootKey(r.mainRoot ?? r.root) === rootKey(root);
		});
	const notes = $derived(sourceNotes(chatsView.sources));

	// A project or worktree row: a selection that stays where it is, whatever the sessions do.
	function chooseProject(p: ProjectRow) {
		sessions.unfollow();
		void projects.select(p.id);
		onchosen?.();
	}
	// A session row: the selection follows that session's moves (ADR-0083 §3).
	function chooseAgent(a: AgentRow, workspace: string | null) {
		sessions.select(a.key);
		sessions.follow(a.key);
		if (workspace !== null) {
			void projects.select(workspace);
			onchosen?.();
		}
	}
	// A chat that is not live: select its worktree's workspace, else its project's, when the list
	// holds it open; it opens none (the row then does nothing).
	function chooseChat(r: ChatRow) {
		if (r.workspace === null) return;
		sessions.unfollow();
		void projects.select(r.workspace);
		onchosen?.();
	}
	async function copy(r: ChatRow) {
		if (r.resume === undefined) return;
		try {
			await navigator.clipboard.writeText(r.resume);
			copied = r.key;
			shownCommand = null;
		} catch {
			// Refused or unavailable: show the command, selectable, to copy by hand.
			shownCommand = { key: r.key, text: r.resume };
		}
	}
	function open(key: string) {
		opened = new Set([...opened, key]);
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
	async function release(workspace: string) {
		const f = await sessions.release({ workspace });
		releaseError = f === null ? null : failureText(f);
	}
	const worktreeName = (id: string) => rows.find((r) => r.id === id)?.name;
	const age = (startedAt: string) => duration(now - Date.parse(startedAt));
	const dotLabel = (p: ProjectRow) => `${p.state}${p.trusted ? '' : ', not trusted'}`;
</script>

{#snippet agentRow(a: AgentRow, workspace: string | null, where: string | null, title: string)}
	<li class="agentli">
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
			<span class="label" title={a.label}>{title}</span>
			<span class="state">{#if a.unread}<span class="unread" title="Finished since you last looked"></span>{/if}</span>
			{#if where !== null || occupiesText(a, worktreeName) !== ''}
				<span class="meta">
					{#if where !== null}<span class="where" title="Worktree">{where}</span>{/if}
					{#if occupiesText(a, worktreeName) !== ''}
						<span class="holds" title="Holds this worktree or device, so that no other chat writes or acts there">holds {occupiesText(a, worktreeName)}</span>
					{/if}
					{#if a.idle && occupiesText(a, worktreeName) !== ''}<span class="stale" title="No request for a while: another chat may take over what it holds">idle holder</span>{/if}
				</span>
			{/if}
		</button>
		<!-- A held worktree always has its holder in it, so its Release is on the holder's row. -->
		{#if a.occupies}
			{@const held = a.occupies}
			<button type="button" class="release" title="End the occupation of {worktreeName(held) ?? 'this worktree'}; the session stays where it is" disabled={!connected} onclick={() => void release(held)}>Release</button>
		{/if}
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
	<header class="panel-head">
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

	{#if view && rows.length === 0}
		<p class="note">No projects yet.</p>
	{/if}

	<ul class="list">
		{#each menu as g (rootKey(g.root))}
			{@const agentsHere = sessionsIn(g.root)}
			{@const working = agentsHere.filter((x) => x.running).length}
			{@const p = rows.find((x) => x.id === g.id)}
			{@const worktrees = rows.filter((x) => x.mainRoot !== undefined && rootKey(x.mainRoot) === rootKey(g.root))}
			<li data-project={g.root}>
				{#if p}
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
				{#if worktrees.length > 0}
					<ul class="agents worktrees">
						{#each worktrees as w (w.id)}
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
									<span class="state"><span class="dot" data-state={w.open ? (w.state === 'ready' ? 'ready' : 'busy') : 'closed'} title={dotLabel(w)}></span></span>
									{#if holder}<span class="meta"><span class="where" title="Another chat may not enter or write here">{heldByText(holder.label, holder.idle)}</span></span>{/if}
								</button>
								{#if holder}
									<button type="button" class="release" title="End the occupation; the session stays where it is" disabled={!connected} onclick={() => void release(w.id)}>Release</button>
								{/if}
								{@render remove(w)}
							</li>
						{/each}
					</ul>
				{/if}
			</li>
		{/each}
	</ul>
	{#if view?.truncated}
		<p class="note">Only the 50 most recent projects are listed.</p>
	{/if}

	<header class="panel-head">
		<h2>Sessions</h2>
	</header>
	{#if chatsView.error}
		<p class="note bad">Chats could not be read: {failureText(chatsView.error)}</p>
	{/if}
	{#each notes as n (n)}
		<p class="note">{n}</p>
	{/each}
	{#if chatsView.truncated}
		<p class="note">Still reading the chat stores; the list completes on the next refresh.</p>
	{/if}
	{#if chatsView.loaded && folders.length === 0}
		<p class="note">No chats yet.</p>
	{/if}
	<ul class="list tree">
		{#each folders as f (f.root ?? '')}
			{@const k = f.root ?? ''}
			{@const part = cut(f.rows, opened.has(k))}
			<li>
				<div class="project group" title={f.root ?? undefined}><Folder size={14} /> <span class="name">{f.name}</span></div>
				<ul class="agents">
					{#each f.connections as c (c.row.key)}
						{@render agentRow(c.row, c.row.workspace, null, `${c.row.label} connected`)}
					{/each}
					{#each part.shown as r (r.key)}
						{#if r.live}
							{@render agentRow(r.live, r.live.workspace, r.worktree, r.title)}
						{:else}
							<li class="line">
								<button type="button" class="agent" title={r.title} onclick={() => chooseChat(r)}>
									<MessageSquare size={12} />
									<span class="label" title={r.title}>{r.title}</span>
									<span class="state"><span class="when">{agoText(now - Date.parse(r.updatedAt))}</span></span>
									{#if r.worktree !== null}<span class="meta"><span class="where" title="Worktree">{r.worktree}</span></span>{/if}
								</button>
								{#if r.resume !== undefined}
									<button type="button" class="release" title="Copy the command that resumes this chat" onclick={() => void copy(r)}>
										{copied === r.key ? 'Copied' : 'Copy'}
									</button>
								{/if}
							</li>
							{#if shownCommand?.key === r.key}
								<li><input class="cmd" readonly aria-label="Resume command" value={shownCommand.text} onfocus={(e) => e.currentTarget.select()} /></li>
							{/if}
						{/if}
					{/each}
					{#if part.more > 0}
						<li><button type="button" class="agent more" onclick={() => open(k)}>{part.more} more</button></li>
					{/if}
				</ul>
			</li>
		{/each}
	</ul>
</section>

<style>
	.projects {
		display: flex;
		flex-direction: column;
		gap: var(--sp-3);
		min-height: 0;
		padding: 0 0 var(--sp-4);
		font-size: var(--fs-base);
	}
	header {
		justify-content: space-between;
		gap: var(--sp-4);
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
	.note {
		margin: 0;
		padding: 0 var(--sp-4);
		font-size: var(--fs-base);
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
		padding: 0 var(--sp-3);
	}
	/* A row and its action: the action is the last column and never drops below the row. */
	.line,
	.agentli {
		display: flex;
		align-items: flex-start;
		gap: var(--sp-2);
	}
	.agentli {
		flex-wrap: wrap;
	}
	.line > :first-child,
	.agentli > .agent {
		flex: 1 1 0;
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
	.project {
		display: flex;
		align-items: center;
		gap: var(--sp-3);
		width: 100%;
		min-height: var(--row);
		padding: 0 var(--sp-4);
		border: 0;
		border-radius: var(--radius-control);
		background: transparent;
		color: var(--text);
		text-align: left;
	}
	/* A session row on the sketch's grid: dot or icon, title, state at the right; a second
	   line in mono for worktree, holds and age. */
	.agent {
		display: grid;
		grid-template-columns: 14px minmax(0, 1fr) auto;
		align-items: center;
		gap: 2px var(--sp-4);
		width: 100%;
		min-height: var(--row);
		padding: var(--sp-2) var(--sp-4);
		border: 0;
		border-radius: var(--radius-control);
		background: transparent;
		color: var(--text-2);
		text-align: left;
	}
	.project:hover:not(:disabled, .selected),
	.agent:hover:not(.selected) {
		background: var(--bg-hover);
	}
	.project.group {
		color: var(--text-2);
		font-size: var(--fs-small);
	}
	.name {
		flex: 1;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	/* A chat title is untrusted and long: two lines, then clamped. */
	.label {
		min-width: 0;
		display: -webkit-box;
		-webkit-box-orient: vertical;
		-webkit-line-clamp: 2;
		line-clamp: 2;
		overflow: hidden;
		overflow-wrap: anywhere;
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
	.state {
		display: inline-flex;
		align-items: center;
		justify-self: end;
		gap: var(--sp-3);
	}
	.meta {
		grid-column: 2 / -1;
		display: flex;
		gap: var(--sp-4);
		min-width: 0;
		font-family: var(--font-mono);
		font-size: var(--fs-small);
		color: var(--text-2);
	}
	.when {
		flex: none;
		font-family: var(--font-mono);
		font-size: var(--fs-small);
		color: var(--text-2);
	}
	.cmd {
		width: 100%;
		font-family: var(--font-mono);
		font-size: var(--fs-small);
	}
	.more {
		display: block;
		color: var(--text-2);
	}
	.where,
	.holds,
	.stale {
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.where {
		flex: 0 1 auto;
	}
	.stale {
		flex: none;
		color: var(--warning);
	}
	.release {
		flex: none;
		min-height: 0;
		height: calc(var(--row) - 2px);
		padding: 0 var(--sp-4);
		font-size: var(--fs-small);
	}
	.label.closed {
		color: var(--text-3);
	}
	.op {
		flex: 0 0 100%;
		padding: 0 var(--sp-4) 0 calc(var(--sp-4) + 14px + var(--sp-4));
		font-family: var(--font-mono);
		font-size: var(--fs-small);
		color: var(--text-2);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
</style>
