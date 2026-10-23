<script lang="ts">
	import type { WorkspaceModel, WorkspaceView } from './workspace';
	import type { TrustModel } from '../start/trust';
	import Trust from '../start/Trust.svelte';
	import { failureShort, failureText } from './failure';

	// The workspace panel (plan m2b D10, m2c step 10): the tab's project, what the daemon
	// says about it, Trust while it is not, and the way to the projects panel. Every field is
	// a text node.
	interface Props {
		model: WorkspaceModel;
		trust: TrustModel;
		onprojects: () => void;
		/** One line for the toolbar: the name, a short state, long messages in a tooltip; a click on the name opens the sessions column (D34). */
		compact?: boolean;
	}
	let { model, trust, onprojects, compact = false }: Props = $props();

	let view = $state.raw<WorkspaceView | null>(null);

	$effect(() => {
		const m = model;
		const read = () => {
			view = m.view;
		};
		read();
		return m.subscribe(read);
	});
</script>

{#if compact}
	<section class="compact">
		{#if view?.info}
			<button type="button" class="name" title="{view.info.name} ({view.info.id}), {view.root}: show the sessions" onclick={onprojects}>{view.info.name}</button>
			<span class="dot" class:ready={view.info.state === 'ready'} class:degraded={view.info.state === 'degraded'} aria-hidden="true"></span>
			<span class="state" title={view.info.state}>{view.info.state}</span>
			{#if view.info.degradedBy}
				<span class="bad" title={failureText(view.info.degradedBy)}>degraded</span>
			{/if}
		{:else if view?.opening}
			<span class="state">Opening...</span>
		{/if}
		{#if view?.error}
			<span class="bad" title={failureText(view.error)}>{failureShort(view.error)}</span>
		{/if}
		{#if view?.closedByOther || (view && view.info === null && view.error && !view.opening)}
			<span class="state">{view.closedByOther ? 'Closed by another client' : 'Not open'}</span>
			<button type="button" disabled={view.opening} onclick={() => void model.open()}>
				{view.closedByOther ? 'Reopen' : 'Open'}
			</button>
		{/if}
	</section>
{:else}
<section>
	<h2>Workspace</h2>
	<button type="button" onclick={onprojects}>All projects</button>
	{#if view?.opening}
		<p>Opening...</p>
	{/if}
	{#if view?.error}
		<p>{failureText(view.error)}</p>
	{/if}
	{#if view?.closedByOther || (view && view.info === null && view.error && !view.opening)}
		<p>{view.closedByOther ? 'This workspace was closed by another client.' : 'The workspace is not open.'}</p>
		<button type="button" disabled={view.opening} onclick={() => void model.open()}>
			{view.closedByOther ? 'Reopen' : 'Open'}
		</button>
	{/if}
	{#if view?.info}
		<p>
			{view.info.name} ({view.info.id}), {view.root}: {view.info.state}, {view.info.trusted ? 'trusted' : 'not trusted'}
		</p>
		{#if view.info.degradedBy}
			<p>Degraded: {failureText(view.info.degradedBy)}</p>
		{/if}
		{#if !view.info.trusted && !view.closedByOther}
			<Trust model={trust} />
		{/if}
	{/if}
	{#if view && view.seen.length > 0}
		<p>
			Workspaces seen:
			{#each view.seen as w (w.id)}
				{w.id} {w.state};
			{/each}
		</p>
	{/if}
</section>
{/if}

<style>
	.compact {
		display: flex;
		align-items: center;
		gap: var(--sp-3);
		white-space: nowrap;
	}
	.name {
		border-color: transparent;
		background: transparent;
		font-size: var(--fs-title);
		font-weight: 600;
		padding: 0 var(--sp-3);
		min-width: 0;
		max-width: 220px;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	/* A state dot: colour means state (green ready, amber degraded, hollow otherwise). */
	.dot {
		flex: none;
		width: 8px;
		height: 8px;
		box-sizing: border-box;
		border-radius: 50%;
		border: 1.5px solid var(--text-3);
	}
	.dot.ready {
		border: 0;
		background: var(--success);
	}
	.dot.degraded {
		border: 0;
		background: var(--warning);
	}
	.state {
		color: var(--text-2);
		font-size: var(--fs-small);
		font-weight: 500;
	}
	.bad {
		color: var(--danger);
		font-size: var(--fs-small);
	}
</style>
