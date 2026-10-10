<script lang="ts">
	import Bot from '@lucide/svelte/icons/bot';
	import Check from '@lucide/svelte/icons/check';
	import Folder from '@lucide/svelte/icons/folder';
	import FolderPlus from '@lucide/svelte/icons/folder-plus';
	import X from '@lucide/svelte/icons/x';
	import { failureText } from '../build/failure';
	import type { ProjectsModel, ProjectsView, ProjectRow } from './projects';
	import { duration, type AgentRow, type SessionsModel, type SessionsView } from './sessions';
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
	}
	let { projects, sessions, selected, onchosen }: Props = $props();

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
	const known = $derived(new Set(rows.map((r) => r.id)));
	const agentsOf = (id: string | null): AgentRow[] =>
		agents.rows.filter((r) => (id === null ? r.workspace === null || !known.has(r.workspace) : r.workspace === id));
	const loose = $derived(agentsOf(null));

	function chooseProject(p: ProjectRow) {
		void projects.select(p.id);
		onchosen?.();
	}
	function chooseAgent(a: AgentRow) {
		sessions.select(a.key);
		if (a.workspace !== null && known.has(a.workspace)) {
			void projects.select(a.workspace);
			onchosen?.();
		}
	}
	function openTyped(e: SubmitEvent) {
		e.preventDefault();
		void projects.openRoot(typed);
	}
	const age = (startedAt: string) => duration(now - Date.parse(startedAt));
	const dotLabel = (p: ProjectRow) => `${p.state}${p.trusted ? '' : ', not trusted'}`;
</script>

{#snippet agentRow(a: AgentRow)}
	<li>
		<button type="button" class="agent" onclick={() => chooseAgent(a)}>
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
			{#if a.unread}<span class="unread" title="Finished since you last looked"></span>{/if}
		</button>
		{#each a.operations as o (o.id)}
			<div class="op">{o.capability} · {age(o.startedAt)}</div>
		{/each}
	</li>
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

	{#if view && rows.length === 0 && loose.length === 0}
		<p class="note">No projects yet.</p>
	{/if}

	<ul class="list">
		{#each rows as p (p.id)}
			{@const mine = agentsOf(p.id)}
			{@const working = mine.filter((a) => a.running && !a.you).length}
			<li>
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
					{#if mine.some((a) => !a.you)}
						<span class="count" class:working={working > 0} title="{working} working">{mine.filter((a) => !a.you).length}</span>
					{/if}
					<span class="dot" data-state={p.open ? (p.state === 'ready' ? 'ready' : 'busy') : 'closed'} title={dotLabel(p)}></span>
				</button>
				{#if mine.length > 0}
					<ul class="agents">
						{#each mine as a (a.key)}
							{@render agentRow(a)}
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
						{@render agentRow(a)}
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
