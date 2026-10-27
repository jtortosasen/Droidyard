<script lang="ts">
	import Check from '@lucide/svelte/icons/check';
	import X from '@lucide/svelte/icons/x';
	import ClientTag, { type ClientKind } from './ClientTag.svelte';
	import { duration, occupiesText, type AgentRow } from './sessions';
	import Spinner from './Spinner.svelte';

	// One live session: the same line nested under its tree and as a connected chat in a folder
	// (ADR-0134). Glyph by state (running, completed, failed, connected and idle), the client's
	// tag, the title (a chat's or `<label> connected`: untrusted text, a text node), what it
	// holds and its running operations. The followed one carries the selection edge.
	interface Props {
		row: AgentRow;
		title: string;
		kind: ClientKind;
		/** The session the selection follows (ADR-0083 §3). */
		followed: boolean;
		now: number;
		/** The connection is open: Release needs it. */
		connected: boolean;
		/** Show this session's own Release (the held tree's row has one when it is the held one). */
		releasable: boolean;
		worktreeName: (workspace: string) => string | undefined;
		/** A chat's worktree and age, in the second line. */
		where?: string | null;
		when?: string;
		/** The title wraps to two lines (a chat's), else one. */
		wrap?: boolean;
		onchoose: () => void;
		onrelease: (workspace: string) => void;
	}
	let { row, title, kind, followed, now, connected, releasable, worktreeName, where = null, when, wrap = false, onchoose, onrelease }: Props = $props();

	const holds = $derived(occupiesText(row, worktreeName));
	</script>

<li class="session">
	<div class="line" class:selected={followed}>
		<button type="button" class="pick" aria-current={followed ? 'true' : undefined} onclick={onchoose}>
			<span class="glyph">
				{#if row.running}
					<Spinner size={11} />
				{:else if row.result === 'completed'}
					<span class="ok" title="Last operation succeeded"><Check size={12} /></span>
				{:else if row.result === 'failed'}
					<span class="fail" title="Last operation failed"><X size={12} /></span>
				{:else}
					<span class="idle" title="Connected, idle"></span>
				{/if}
			</span>
			<span class="head">
				<ClientTag {kind} />
				<span class="title" class:wrap title={row.label}>{title}</span>
				{#if row.unread}<span class="unread" title="Finished since you last looked"></span>{/if}
				{#if when !== undefined}<span class="when">{when}</span>{/if}
			</span>
			{#if where !== null || holds !== ''}
				<span class="meta">
					{#if where !== null}<span class="where" title="Worktree">{where}</span>{/if}
					{#if holds !== ''}<span class="holds" title="Holds this worktree or device, so that no other chat writes or acts there">holds {holds}</span>{/if}
					{#if row.idle && holds !== ''}<span class="stale" title="No request for a while: another chat may take over what it holds">idle holder</span>{/if}
				</span>
			{/if}
		</button>
		{#if releasable && row.occupies}
			{@const held = row.occupies}
			<button type="button" class="release" title="End the occupation of {worktreeName(held) ?? 'this worktree'}; the session stays where it is" disabled={!connected} onclick={() => onrelease(held)}>Release</button>
		{/if}
	</div>
	{#each row.operations as o (o.id)}
		<div class="op">{o.capability} · {duration(now - Date.parse(o.startedAt))}</div>
	{/each}
</li>

<style>
	.session {
		list-style: none;
	}
	.line {
		display: flex;
		align-items: flex-start;
		gap: var(--sp-2);
		border-radius: var(--radius-control);
	}
	.pick {
		flex: 1 1 0;
		min-width: 0;
		display: grid;
		grid-template-columns: 14px minmax(0, 1fr);
		align-items: center;
		gap: 1px var(--sp-4);
		min-height: var(--row);
		padding: var(--sp-2) var(--sp-4);
		border: 0;
		border-radius: var(--radius-control);
		background: transparent;
		color: var(--text);
		font-weight: 400;
		text-align: left;
	}
	.line:not(.selected) .pick:hover {
		background: var(--bg-hover);
	}
	.glyph {
		display: inline-grid;
		place-items: center;
		width: 14px;
	}
	.idle {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		background: var(--success);
	}
	.ok {
		display: inline-flex;
		color: var(--success);
	}
	.fail {
		display: inline-flex;
		color: var(--danger);
	}
	.head {
		display: flex;
		align-items: center;
		gap: var(--sp-3);
		min-width: 0;
	}
	.title {
		flex: 1 1 0;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.title.wrap {
		display: -webkit-box;
		-webkit-box-orient: vertical;
		-webkit-line-clamp: 2;
		line-clamp: 2;
		white-space: normal;
		overflow-wrap: anywhere;
	}
	.unread {
		flex: none;
		width: 7px;
		height: 7px;
		border-radius: 50%;
		background: var(--accent);
	}
	.when {
		flex: none;
		font-family: var(--font-mono);
		font-size: var(--fs-small);
		color: var(--text-2);
	}
	.meta {
		grid-column: 2;
		display: flex;
		gap: var(--sp-4);
		min-width: 0;
		font-family: var(--font-mono);
		font-size: var(--fs-small);
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
	.stale {
		flex: none;
		color: var(--warning);
	}
	.release {
		flex: none;
		min-height: 0;
		height: calc(var(--row) - 2px);
		margin-top: var(--sp-2);
		padding: 0 var(--sp-4);
		font-size: var(--fs-small);
	}
	.op {
		padding: 0 var(--sp-4) 0 calc(var(--sp-4) + 14px + var(--sp-4));
		font-family: var(--font-mono);
		font-size: var(--fs-small);
		color: var(--text-2);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
</style>
