<script lang="ts">
	import Folder from '@lucide/svelte/icons/folder';
	import { failureText } from '../build/failure';
	import ClientTag, { type ClientKind } from './ClientTag.svelte';
	import { agoText, cut, sourceNotes, type Chat, type ChatRow, type ChatsView, type Folder as ChatFolder } from './chats';
	import type { ProjectsModel } from './projects';
	import { occupiesText, type SessionsModel } from './sessions';
	import SessionLine from './SessionLine.svelte';

	// Every chat by project folder (ADR-0134 §3, §4): a head with the folder's name and one tag
	// per live connection no chat claims; rows with the chat's client, title and age. A chat a
	// live session names is a connected row (full contrast, its state, holds, Release, the edge
	// when followed); one nothing names, in a folder with no unclaimed session of its client, is
	// on the quiet surface tagged "not connected"; the rest are plain. Titles are untrusted text
	// (ADR-0015 §1): text nodes.
	interface Props {
		projects: ProjectsModel;
		sessions: SessionsModel;
		folders: readonly ChatFolder[];
		chats: ChatsView;
		following: string | null;
		now: number;
		connected: boolean;
		worktreeName: (workspace: string) => string | undefined;
		/** The sessions nested on the tab's trees above: their Release is there, not repeated here. */
		nested: ReadonlySet<string>;
		onchosen?: () => void;
		onrelease: (workspace: string) => void;
	}
	let { projects, sessions, folders, chats, following, now, connected, worktreeName, nested, onchosen, onrelease }: Props = $props();

	// "N more" opened, per folder; in memory only.
	let opened = $state.raw<ReadonlySet<string>>(new Set());
	let copied = $state<string | null>(null);
	let shownCommand = $state<{ key: string; text: string } | null>(null);
	const notes = $derived(sourceNotes(chats.sources));

	const kindOf = (store: Chat['store']): ClientKind => (store === 'cursor-ide' ? 'cursor' : store);

	function followSession(key: string, workspace: string | null) {
		sessions.select(key);
		sessions.follow(key);
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
</script>

<section class="sessions" aria-label="Sessions">
	<h2>Sessions</h2>
	{#if chats.error}
		<p class="note bad">Chats could not be read: {failureText(chats.error)}</p>
	{/if}
	{#each notes as n (n)}
		<p class="note">{n}</p>
	{/each}
	{#if chats.truncated}
		<p class="note">Still reading the chat stores; the list completes on the next refresh.</p>
	{/if}
	{#if chats.loaded && folders.length === 0}
		<p class="note">No chats yet.</p>
	{/if}
	<ul class="folders">
		{#each folders as f (f.root ?? '')}
			{@const k = f.root ?? ''}
			{@const part = cut(f.rows, opened.has(k))}
			<li class="folder">
				<div class="fhead" class:current={f.current} title={f.root ?? undefined}>
					<span class="ficon"><Folder size={14} /></span>
					<span class="fname">{f.name}</span>
					{#if f.rows.length > 0}
						<span class="count" aria-label="{f.rows.length} {f.rows.length === 1 ? 'chat' : 'chats'}">{f.rows.length}</span>
					{/if}
					{#each f.connections as c (c.row.key)}
						{@const holds = occupiesText(c.row, worktreeName)}
						<ClientTag kind={c.client} label={c.row.label} connected running={c.row.running} title={holds === '' ? 'Connected to Droidyard' : `Connected to Droidyard, holds ${holds}`} />
					{/each}
				</div>
				<ul class="rows">
					{#each part.shown as r (r.key)}
						{#if r.live}
							{@const live = r.live}
							<SessionLine
								row={live}
								title={r.title}
								kind={kindOf(r.tag)}
								followed={following === live.key}
								{now}
								{connected}
								releasable={!nested.has(live.key)}
								{worktreeName}
								where={r.worktree}
								when={agoText(now - Date.parse(r.updatedAt))}
								onchoose={() => followSession(live.key, live.workspace)}
								{onrelease}
							/>
						{:else}
							<li class="chat" class:quiet={r.presence === 'not-connected'}>
								<div class="line">
									<button type="button" class="pick" onclick={() => chooseChat(r)}>
										<span class="glyph">
											{#if r.presence === 'not-connected'}<span class="ring"></span><span class="sr">not connected</span>{/if}
										</span>
										<span class="title">{r.title}</span>
										<span class="meta" class:withaction={r.resume !== undefined}>
											<ClientTag kind={kindOf(r.tag)} quiet={r.presence === 'not-connected'} />
											{#if r.worktree !== null}<span class="where" title="Worktree">{r.worktree}</span>{/if}
											<span class="when">{agoText(now - Date.parse(r.updatedAt))}</span>
											{#if r.presence === 'not-connected'}<span class="absent" title="No live Droidyard session: resume it in its client">not connected</span>{/if}
										</span>
									</button>
									{#if r.resume !== undefined}
										<button type="button" class="copy" title="Copy the command that resumes this chat" onclick={() => void copy(r)}>
											{copied === r.key ? 'Copied' : 'Copy'}
										</button>
									{/if}
								</div>
								{#if shownCommand?.key === r.key}
									<input class="cmd" readonly aria-label="Resume command" value={shownCommand.text} onfocus={(e) => e.currentTarget.select()} />
								{/if}
							</li>
						{/if}
					{/each}
					{#if part.more > 0}
						<li><button type="button" class="more" onclick={() => open(k)}>{part.more} more</button></li>
					{/if}
				</ul>
			</li>
		{/each}
	</ul>
</section>

<style>
	.sessions {
		display: flex;
		flex-direction: column;
		gap: var(--sp-2);
		min-height: 0;
		padding-top: var(--sp-5);
		border-top: 1px solid var(--border);
		font-size: var(--fs-base);
	}
	h2 {
		padding: 0 var(--sp-6);
	}
	.note {
		margin: 0;
		padding: 0 var(--sp-6);
		color: var(--text-2);
	}
	.note.bad {
		color: var(--danger);
	}
	.folders,
	.rows {
		list-style: none;
		margin: 0;
		padding: 0;
	}
	.folders {
		display: flex;
		flex-direction: column;
		gap: var(--sp-5);
		padding: 0 var(--sp-3);
	}
	.fhead {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: var(--sp-3);
		padding: var(--sp-2) var(--sp-4);
		color: var(--text);
		font-size: var(--fs-base);
		font-weight: 600;
	}
	.ficon {
		display: inline-flex;
		color: var(--text-2);
	}
	.fhead.current .ficon {
		color: var(--accent);
	}
	.fname {
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.count {
		flex: none;
		min-width: 16px;
		padding: 0 var(--sp-3);
		border-radius: 8px;
		background: var(--bg-active);
		color: var(--text-2);
		font-size: var(--fs-label);
		font-weight: 400;
		text-align: center;
	}
	.rows {
		display: flex;
		flex-direction: column;
		gap: 1px;
	}
	.line {
		position: relative;
	}
	/* The same grid as a live session's line, so every row shares one left edge. */
	.pick {
		display: grid;
		grid-template-columns: 14px minmax(0, 1fr);
		align-items: start;
		gap: 1px var(--sp-4);
		width: 100%;
		min-height: var(--row);
		padding: var(--sp-3) var(--sp-4);
		border: 0;
		border-radius: var(--radius-control);
		background: transparent;
		color: var(--text);
		font-weight: 400;
		text-align: left;
	}
	.pick:hover {
		background: var(--bg-hover);
	}
	.quiet .pick {
		background: var(--bg-quiet);
		color: var(--text-2);
	}
	.quiet .pick:hover {
		background: var(--bg-hover);
	}
	.glyph {
		display: inline-grid;
		place-items: center;
		width: 14px;
		height: calc(var(--fs-base) * var(--lh-base));
	}
	.ring {
		box-sizing: border-box;
		width: 8px;
		height: 8px;
		border: 1.5px solid var(--text-3);
		border-radius: 50%;
	}
	.title {
		min-width: 0;
		display: -webkit-box;
		-webkit-box-orient: vertical;
		-webkit-line-clamp: 2;
		line-clamp: 2;
		overflow: hidden;
		overflow-wrap: anywhere;
	}
	.meta {
		grid-column: 2;
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: 0 var(--sp-4);
		min-width: 0;
		font-family: var(--font-mono);
		font-size: var(--fs-small);
		color: var(--text-2);
	}
	.meta.withaction {
		padding-right: 44px;
	}
	.where {
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.when {
		flex: none;
	}
	.absent {
		flex: none;
		padding: 0 var(--sp-3);
		border: 1px solid var(--border);
		border-radius: 3px;
		font-family: var(--font-ui);
		font-size: var(--fs-label);
		color: var(--text-3);
	}
	/* Copy waits at the row's end until the row is pointed at or focused; it stays in the tab order. */
	.copy {
		position: absolute;
		right: var(--sp-3);
		bottom: var(--sp-2);
		min-height: 0;
		height: calc(var(--row) - 6px);
		padding: 0 var(--sp-3);
		border: 0;
		background: transparent;
		color: var(--accent);
		font-size: var(--fs-small);
		opacity: 0;
	}
	.line:hover .copy,
	.line:focus-within .copy {
		opacity: 1;
	}
	.copy:hover {
		text-decoration: underline;
	}
	.cmd {
		width: 100%;
		font-family: var(--font-mono);
		font-size: var(--fs-small);
	}
	.more {
		min-height: 0;
		height: var(--row);
		padding: 0 var(--sp-4);
		border: 0;
		background: transparent;
		color: var(--text-2);
	}
</style>
