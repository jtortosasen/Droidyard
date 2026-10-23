<script lang="ts">
	import type { Component } from 'svelte';
	import Archive from '@lucide/svelte/icons/archive';
	import ArrowDown from '@lucide/svelte/icons/arrow-down';
	import ArrowDownToLine from '@lucide/svelte/icons/arrow-down-to-line';
	import ArrowUp from '@lucide/svelte/icons/arrow-up';
	import ArrowUpFromLine from '@lucide/svelte/icons/arrow-up-from-line';
	import Download from '@lucide/svelte/icons/download';
	import GitBranch from '@lucide/svelte/icons/git-branch';
	import GitBranchPlus from '@lucide/svelte/icons/git-branch-plus';
	import LocateFixed from '@lucide/svelte/icons/locate-fixed';
	import X from '@lucide/svelte/icons/x';
	import Spinner from '../projects/Spinner.svelte';
	import { headSource } from './branchops';
	import { activate, TOOLBAR, type ToolbarId } from './controls';
	import type { FetchView } from './fetch';
	import type { PullView } from './pull';
	import { summaryText } from './pull';
	import { headDisplay, type RefsHead } from './refs';

	// The History tab's operations toolbar, after SourceGit's (plan m2n step 6; the owner's item
	// 10): Fetch, Pull, Push, Stash, a separator, Create Branch; at the right end the current
	// branch's name with its ahead and behind badge, and Navigate to HEAD. A write control is
	// shown disabled until the part that enables it (controls.ts); a click goes through
	// `activate`, so a disabled control reaches nothing. Create Branch (step P3) starts at HEAD and
	// waits for the refs to say where HEAD is. Fetch (step P2) is enabled: while a fetch
	// is in flight it shows a spinner with Queued or Fetching, is disabled, and a Cancel beside it
	// ends the operation (`data-op-state`, `data-op-cancel`). Pull (plan m2n R3) works the same way
	// (`data-pull-cancel`) and shows, under the toolbar, what it did or why it did not (`data-pull-result`,
	// `data-pull-error`, `data-pull-blocked`). Pull and Push carry a dot when the branch is behind or ahead. The branch name is git's text, in an isolated text node.
	interface Props {
		/** HEAD as the refs read it; null until they arrive. */
		head: RefsHead | null;
		/** Navigate to HEAD was pressed. */
		onnavigate: () => void;
		/** Part 2's seam: an enabled control was pressed. */
		onrun?: (id: ToolbarId) => void;
		/** The Fetch in flight, or its last failure. */
		fetchView?: FetchView;
		/** Cancel was pressed while a fetch is in flight. */
		oncancel?: () => void;
		/** The Pull in flight, its last result or failure. */
		pullView?: PullView;
		/** Cancel was pressed while a pull is in flight. */
		onpullcancel?: () => void;
		/** The result, failure or block of the last pull was dismissed. */
		onpulldismiss?: () => void;
	}
	let { head, onnavigate, onrun, fetchView, oncancel, pullView, onpullcancel, onpulldismiss }: Props = $props();

	const fetching = $derived(fetchView?.busy ?? false);
	/** Create Branch needs HEAD's commit: unknown until the refs arrive, absent in a repository with no commit yet. */
	const noHead = $derived(headSource(head) === null);
	const fetchLabel = $derived(fetchView?.state === 'queued' ? 'Queued…' : fetching ? 'Fetching…' : 'Fetch');
	const pulling = $derived(pullView?.busy ?? false);
	const pullLabel = $derived(pullView?.state === 'queued' ? 'Queued…' : pulling ? 'Pulling…' : 'Pull');
	const opState = (id: ToolbarId) => (id === 'fetch' ? (fetchView?.state ?? 'idle') : id === 'pull' ? (pullView?.state ?? 'idle') : undefined);

	const ICONS: Record<ToolbarId, Component> = {
		fetch: Download,
		pull: ArrowDownToLine,
		push: ArrowUpFromLine,
		stash: Archive,
		'create-branch': GitBranchPlus,
	};

	const shown = $derived(head === null ? null : headDisplay(head));
	const dot = (id: ToolbarId) => (id === 'pull' ? (shown?.behind ?? 0) > 0 : id === 'push' ? (shown?.ahead ?? 0) > 0 : false);
	const upstreamTitle = $derived(
		shown === null
			? ''
			: shown.upstream === undefined
				? 'No upstream'
				: shown.upstreamGone
					? `Upstream ${shown.upstream} is gone`
					: `Upstream ${shown.upstream}: ${shown.ahead} ahead, ${shown.behind} behind`,
	);
</script>

<div class="toolbar" role="toolbar" aria-label="Git operations">
	{#each TOOLBAR as c (c.id)}
		{@const Icon = ICONS[c.id]}
		{#if c.id === 'create-branch'}<span class="sep" role="separator" aria-orientation="vertical"></span>{/if}
		<button
			type="button"
			class="tool"
			data-toolbar={c.id}
			data-dot={dot(c.id) ? '' : undefined}
			data-op-state={opState(c.id)}
			disabled={!c.enabled || (c.id === 'fetch' && fetching) || (c.id === 'pull' && pulling) || (c.id === 'create-branch' && noHead)}
			title={c.id === 'fetch' && fetching
				? fetchView?.remote == null
					? 'Fetching every remote'
					: `Fetching ${fetchView.remote}`
				: c.id === 'pull' && pulling
					? 'Pulling the current branch'
					: c.id === 'create-branch' && noHead
					? head === null
						? 'Reading the refs…'
						: 'HEAD has no commit yet'
					: c.title}
			onclick={() => activate(c, () => onrun?.(c.id))}
		>
			{#if (c.id === 'fetch' && fetching) || (c.id === 'pull' && pulling)}<Spinner size={12} />{:else}<Icon size={14} />{/if}
			<span>{c.id === 'fetch' ? fetchLabel : c.id === 'pull' ? pullLabel : c.label}</span>
			{#if dot(c.id)}<span class="dot" aria-hidden="true"></span>{/if}
		</button>
		{#if c.id === 'fetch' && fetching}
			<button
				type="button"
				class="tool"
				data-op-cancel
				disabled={fetchView?.cancelling}
				title={fetchView?.cancelling ? 'Cancelling…' : 'Cancel the fetch'}
				onclick={() => oncancel?.()}
			>
				<X size={14} />
				<span>Cancel</span>
			</button>
		{/if}
		{#if c.id === 'pull' && pulling}
			<button
				type="button"
				class="tool"
				data-pull-cancel
				disabled={pullView?.cancelling}
				title={pullView?.cancelling ? 'Cancelling…' : 'Cancel the pull'}
				onclick={() => onpullcancel?.()}
			>
				<X size={14} />
				<span>Cancel</span>
			</button>
		{/if}
	{/each}
	<span class="fill"></span>
	{#if shown}
		<span
			class="branch"
			data-branch-display
			data-ahead={shown.ahead}
			data-behind={shown.behind}
			data-detached={shown.detached ? '' : undefined}
			title={upstreamTitle}
		>
			<GitBranch size={14} />
			<span class="name">{shown.name}</span>
			{#if shown.upstream !== undefined && !shown.upstreamGone}
				<span class="counts">
					<span class="count" class:zero={shown.ahead === 0} data-ahead-badge><ArrowUp size={11} />{shown.ahead}</span>
					<span class="count" class:zero={shown.behind === 0} data-behind-badge><ArrowDown size={11} />{shown.behind}</span>
				</span>
			{:else if shown.upstreamGone}
				<span class="counts gone" data-upstream-gone>gone</span>
			{/if}
		</span>
	{/if}
	<button
		type="button"
		class="tool"
		data-toolbar="navigate-head"
		disabled={head === null}
		title={head === null ? 'Reading the refs…' : 'Navigate to HEAD'}
		onclick={onnavigate}
	>
		<LocateFixed size={14} />
		<span>Navigate to HEAD</span>
	</button>
</div>
{#if pullView && (pullView.blocked.length > 0 || pullView.failure || pullView.result)}
	<div class="pull-note" class:bad={pullView.blocked.length > 0 || pullView.failure !== null} role={pullView.result ? 'status' : 'alert'}>
		<div class="what">
			{#if pullView.blocked.length > 0}
				<p class="msg" data-pull-blocked>{pullView.blockedText}</p>
				<ul class="files" aria-label="Files with unsaved changes">
					{#each pullView.blocked as path (path)}
						<li>{path}</li>
					{/each}
				</ul>
			{:else if pullView.failure}
				<p class="msg" data-pull-error>{pullView.failure.code ? `${pullView.failure.code}: ` : ''}{pullView.failure.message}</p>
				{#if pullView.failure.cause}<pre class="cause">{pullView.failure.cause}</pre>{/if}
				{#if pullView.failure.nextStep}<p class="next">{pullView.failure.nextStep}</p>{/if}
			{:else if pullView.result}
				<p data-pull-result data-up-to-date={pullView.result.upToDate ? '' : undefined}>{summaryText(pullView.result)}</p>
			{/if}
		</div>
		<button type="button" class="icon" aria-label="Dismiss" title="Dismiss" onclick={() => onpulldismiss?.()}><X size={14} /></button>
	</div>
{/if}

<style>
	.toolbar {
		flex: none;
		display: flex;
		align-items: center;
		gap: var(--sp-2);
		padding: var(--sp-2) var(--sp-4);
		border-bottom: 1px solid var(--border);
		font-size: var(--fs-small);
		min-width: 0;
	}
	.tool {
		position: relative;
		display: inline-flex;
		align-items: center;
		gap: var(--sp-2);
		border-color: transparent;
		background: transparent;
		white-space: nowrap;
	}
	.sep {
		align-self: stretch;
		width: 1px;
		margin: var(--sp-1) var(--sp-3);
		background: var(--border);
	}
	.fill {
		flex: 1;
	}
	.dot {
		position: absolute;
		top: 2px;
		right: 2px;
		width: 6px;
		height: 6px;
		border-radius: 50%;
		background: var(--warning);
	}
	.branch {
		display: inline-flex;
		align-items: center;
		gap: var(--sp-3);
		min-width: 0;
		padding: 0 var(--sp-3);
		color: var(--text);
	}
	.name {
		unicode-bidi: isolate;
		min-width: 0;
		max-width: 28ch;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.counts {
		display: inline-flex;
		gap: var(--sp-2);
		font-family: var(--font-mono);
		font-size: var(--fs-small);
	}
	.count {
		display: inline-flex;
		align-items: center;
		padding: 0 var(--sp-2);
		border-radius: var(--radius-control);
		background: var(--accent-soft);
	}
	.count.zero {
		background: transparent;
		color: var(--text-3);
	}
	.gone {
		color: var(--warning);
	}
	.pull-note {
		flex: none;
		display: flex;
		align-items: flex-start;
		gap: var(--sp-3);
		padding: var(--sp-2) var(--sp-4);
		border-bottom: 1px solid var(--border);
		font-size: var(--fs-base);
	}
	.pull-note .what {
		flex: 1;
		min-width: 0;
	}
	.pull-note p,
	.pull-note pre {
		margin: 0;
		overflow-wrap: anywhere;
		white-space: pre-wrap;
		unicode-bidi: isolate;
	}
	.pull-note.bad .msg {
		color: var(--danger);
	}
	.pull-note .cause {
		font-family: var(--font-mono);
		color: var(--text-2);
		max-height: 8em;
		overflow: auto;
	}
	.pull-note .next {
		color: var(--text-2);
	}
	.pull-note .icon {
		display: inline-flex;
		align-items: center;
		border-color: transparent;
		background: transparent;
	}
	.pull-note .files {
		margin: 0;
		padding-left: var(--sp-5);
		font-family: var(--font-mono);
		unicode-bidi: isolate;
	}
</style>
