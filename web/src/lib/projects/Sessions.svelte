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
		onchosen?: () => void;
		onrelease: (workspace: string) => void;
	}
	let { projects, sessions, folders, chats, following, now, connected, worktreeName, onchosen, onrelease }: Props = $props();

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
				<div class="fhead" title={f.root ?? undefined}>
					<Folder size={14} />
					<span class="fname">{f.name}</span>
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
								releasable
								{worktreeName}
								where={r.worktree}
								when={agoText(now - Date.parse(r.updatedAt))}
								wrap
								onchoose={() => followSession(live.key, live.workspace)}
								{onrelease}
							/>
						{:else}
							<li class="chat" class:quiet={r.presence === 'not-connected'}>
								<div class="line">
									<button type="button" class="pick" title={r.title} onclick={() => chooseChat(r)}>
										<span class="top">
											<ClientTag kind={kindOf(r.tag)} />
											<span class="title">{r.title}</span>
											<span class="when">{agoText(now - Date.parse(r.updatedAt))}</span>
										</span>
										{#if r.worktree !== null || r.presence === 'not-connected'}
											<span class="meta">
												{#if r.worktree !== null}<span class="where" title="Worktree">{r.worktree}</span>{/if}
												{#if r.presence === 'not-connected'}<span class="absent" title="No live Droidyard session: resume it in its client">not connected</span>{/if}
											</span>
										{/if}
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
		color: var(--text-2);
		font-size: var(--fs-small);
		font-weight: 600;
	}
	.fname {
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.rows {
		display: flex;
		flex-direction: column;
		gap: 1px;
	}
	.line {
		display: flex;
		align-items: flex-start;
		gap: var(--sp-2);
	}
	.pick {
		flex: 1 1 0;
		min-width: 0;
		display: flex;
		flex-direction: column;
		gap: 1px;
		min-height: var(--row);
		padding: var(--sp-2) var(--sp-4);
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
	.top {
		display: flex;
		align-items: flex-start;
		gap: var(--sp-3);
		min-width: 0;
	}
	.title {
		flex: 1 1 0;
		min-width: 0;
		display: -webkit-box;
		-webkit-box-orient: vertical;
		-webkit-line-clamp: 2;
		line-clamp: 2;
		overflow: hidden;
		overflow-wrap: anywhere;
	}
	.when {
		flex: none;
		font-family: var(--font-mono);
		font-size: var(--fs-small);
		color: var(--text-2);
	}
	.meta {
		display: flex;
		align-items: center;
		gap: var(--sp-4);
		min-width: 0;
		font-family: var(--font-mono);
		font-size: var(--fs-small);
		color: var(--text-2);
	}
	.where {
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
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
	.copy {
		flex: none;
		min-height: 0;
		height: calc(var(--row) - 2px);
		margin-top: var(--sp-2);
		padding: 0 var(--sp-4);
		font-size: var(--fs-small);
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
