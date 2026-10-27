<script lang="ts">
	import Check from '@lucide/svelte/icons/check';
	import X from '@lucide/svelte/icons/x';
	import ClientTag, { type ClientKind } from './ClientTag.svelte';
	import { duration, occupiesText, type AgentRow } from './sessions';
	import Spinner from './Spinner.svelte';

	// One live session: the same line nested under its tree and as a connected chat in a folder
	// (ADR-0134). Row 1 is the title (a chat's, or just "connected" when no chat names the
	// session: untrusted text, a text node) with the glyph of its state before it; row 2, in
	// mono, holds the client's tag, the worktree, the age, what it holds and its Release. Its
	// running operations follow. The followed one carries the selection edge.
	interface Props {
		row: AgentRow;
		title: string;
		/** No chat names this session: the title says only "connected" and the tag carries its numbered label. */
		untitled?: boolean;
		kind: ClientKind;
		/** The session the selection follows (ADR-0083 §3). */
		followed: boolean;
		now: number;
		/** The connection is open: Release needs it. */
		connected: boolean;
		/** Show this session's own Release (not when the tree row above already has the control). */
		releasable: boolean;
		worktreeName: (workspace: string) => string | undefined;
		/** A chat's worktree and age, in the meta row. */
		where?: string | null;
		when?: string;
		onchoose: () => void;
		onrelease: (workspace: string) => void;
	}
	let { row, title, untitled = false, kind, followed, now, connected, releasable, worktreeName, where = null, when, onchoose, onrelease }: Props = $props();

	const holds = $derived(occupiesText(row, worktreeName));
	// A worktree said by what the session holds is not said again as its place.
	const place = $derived(where !== null && row.occupies !== null && where === worktreeName(row.occupies) ? null : where);
	const word = $derived(row.running ? 'running' : row.result === 'completed' ? 'completed' : row.result === 'failed' ? 'failed' : 'connected, idle');
	const canRelease = $derived(releasable && row.occupies !== null);
</script>

<li class="session">
	<div class="line" class:selected={followed}>
		<button type="button" class="pick" aria-current={followed ? 'true' : undefined} onclick={onchoose}>
			<span class="glyph">
				{#if row.running}
					<Spinner size={11} />
				{:else if row.result === 'completed'}
					<span class="ok"><Check size={12} /></span>
				{:else if row.result === 'failed'}
					<span class="fail"><X size={12} /></span>
				{:else}
					<span class="idle"></span>
				{/if}
				<span class="sr">{word}</span>
			</span>
			<span class="title" class:untitled title={row.label}>{untitled ? 'connected' : title}</span>
			<span class="unreadslot">{#if row.unread}<span class="unread" title="Finished since you last looked"></span><span class="sr">unread</span>{/if}</span>
			<span class="meta" class:withaction={canRelease}>
				<ClientTag {kind} label={untitled ? row.label : undefined} />
				{#if place !== null}<span class="where" title="Worktree">{place}</span>{/if}
				{#if when !== undefined}<span class="when">{when}</span>{/if}
				{#if holds !== ''}<span class="holds" title="Holds this worktree or device, so that no other chat writes or acts there">holds {holds}</span>{/if}
				{#if row.idle && holds !== ''}<span class="stale" title="No request for a while: another chat may take over what it holds">idle holder</span>{/if}
			</span>
		</button>
		{#if canRelease && row.occupies}
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
		position: relative;
		border-radius: var(--radius-control);
	}
	.pick {
		display: grid;
		grid-template-columns: 14px minmax(0, 1fr) auto;
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
	.line:not(.selected) .pick:hover {
		background: var(--bg-hover);
	}
	/* On the selection tint the tag's own fill would sink its hue under the floor (ADR-0128 §3):
	   it recedes to an outline, as on a quiet row. */
	.line.selected :global(.tag) {
		background: transparent;
		box-shadow: inset 0 0 0 1px var(--soft);
	}
	.glyph {
		display: inline-grid;
		place-items: center;
		width: 14px;
		height: calc(var(--fs-base) * var(--lh-base));
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
	.title {
		min-width: 0;
		display: -webkit-box;
		-webkit-box-orient: vertical;
		-webkit-line-clamp: 2;
		line-clamp: 2;
		overflow: hidden;
		overflow-wrap: anywhere;
	}
	.title.untitled {
		color: var(--text-2);
	}
	.unreadslot {
		display: inline-flex;
		align-items: center;
		height: calc(var(--fs-base) * var(--lh-base));
	}
	.unread {
		flex: none;
		width: 7px;
		height: 7px;
		border-radius: 50%;
		background: var(--accent);
	}
	.meta {
		grid-column: 2 / -1;
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: 0 var(--sp-4);
		min-width: 0;
		font-family: var(--font-mono);
		font-size: var(--fs-small);
		color: var(--text-2);
	}
	/* Room for the Release at the row's end. */
	.meta.withaction {
		padding-right: 58px;
	}
	.where,
	.holds,
	.stale {
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.when {
		flex: none;
	}
	.stale {
		flex: none;
		color: var(--warning);
	}
	.release {
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
	}
	.release:hover:not(:disabled) {
		text-decoration: underline;
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
