<script lang="ts">
	import { failureText } from '../build/failure';
	import { chatFolders, titlesBySession, type ChatsModel, type ChatsView } from './chats';
	import { projectMenu, type ProjectsModel, type ProjectsView } from './projects';
	import type { SessionsModel, SessionsView } from './sessions';
	import Sessions from './Sessions.svelte';
	import Trees from './Trees.svelte';
	import { treeRows, type TreesModel, type TreesView } from './trees';

	// The left column (ADR-0134): the tab's project as its worktrees with their live sessions
	// (Trees), then every chat by project folder (Sessions). This component reads the models and
	// hands the two parts what they draw; every field, a chat's title above all (untrusted text,
	// ADR-0015 section 1), is a text node there.
	interface Props {
		projects: ProjectsModel;
		sessions: SessionsModel;
		chats: ChatsModel;
		trees: TreesModel;
		/** The workspace the tab shows; null where none is. */
		selected: string | null;
		/** A project or a session row was chosen (the overlay closes). */
		onchosen?: () => void;
		/** The connection is open: Release needs it. */
		connected?: boolean;
	}
	let { projects, sessions, chats, trees, selected, onchosen, connected = true }: Props = $props();

	let view = $state.raw<ProjectsView | null>(null);
	let agents = $state.raw<SessionsView>({ rows: [], runningAgents: 0 });
	let following = $state<string | null>(null);
	let chatsView = $state.raw<ChatsView>({ chats: [], sources: [], truncated: false, error: null, loaded: false });
	let treesView = $state.raw<TreesView>({ described: null, error: null });
	let now = $state(Date.now());
	let root = $state<HTMLElement | null>(null);

	$effect(() => {
		const p = projects;
		const s = sessions;
		const read = () => {
			view = p.view;
			agents = s.view;
			following = s.following;
		};
		read();
		const offP = p.subscribe(read);
		const offS = s.subscribe(read);
		const readChats = () => (chatsView = chats.view);
		readChats();
		const offC = chats.subscribe(readChats);
		const readTrees = () => (treesView = trees.view);
		readTrees();
		const offT = trees.subscribe(readTrees);
		// Durations tick, and a row ages out of the panel without any event.
		const timer = setInterval(() => {
			now = Date.now();
			agents = s.view;
		}, 1000);
		return () => {
			offP();
			offS();
			offC();
			offT();
			clearInterval(timer);
		};
	});

	const rows = $derived(view?.rows ?? []);
	const tab = $derived(selected === null ? null : (rows.find((r) => r.id === selected) ?? null));
	const treeList = $derived(treeRows(tab, rows, treesView.described, agents.rows, titlesBySession(chatsView.chats)));
	const menu = $derived(projectMenu(rows));
	const folders = $derived(chatFolders(chatsView.chats, agents.rows, rows, tab === null ? null : (tab.mainRoot ?? tab.root)));
	const worktreeName = (id: string) => rows.find((r) => r.id === id)?.name;

	// The selected tree or the followed session stays in view as it moves (ADR-0134 §5).
	$effect(() => {
		void selected;
		void following;
		root?.querySelector('[aria-current="true"]')?.scrollIntoView?.({ block: 'nearest' });
	});

	// The owner's release of a worktree a session holds (ADR-0094 §3): it moves no session.
	let releaseError = $state<string | null>(null);
	async function release(workspace: string) {
		const f = await sessions.release({ workspace });
		releaseError = f === null ? null : failureText(f);
	}
</script>

<div class="column" bind:this={root}>
	<Trees
		{projects}
		{sessions}
		{view}
		trees={treeList}
		{menu}
		projectName={tab === null ? null : (treeList[0]?.name ?? tab.name)}
		projectRoot={tab === null ? null : (treeList[0]?.root ?? tab.mainRoot ?? tab.root)}
		{following}
		{now}
		{connected}
		treesError={treesView.error}
		{onchosen}
		onrelease={(w) => void release(w)}
	/>
	{#if releaseError}
		<p class="note bad">Release refused: {releaseError}</p>
	{/if}
	<Sessions {projects} {sessions} {folders} chats={chatsView} {following} {now} {connected} {worktreeName} {onchosen} onrelease={(w) => void release(w)} />
</div>

<style>
	.column {
		display: flex;
		flex-direction: column;
		gap: var(--sp-5);
		min-height: 0;
		padding: 0 0 var(--sp-6);
	}
	.note {
		margin: 0;
		padding: 0 var(--sp-6);
		color: var(--text-2);
	}
	.note.bad {
		color: var(--danger);
	}
</style>
