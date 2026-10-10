<script lang="ts">
	import type { WorkspaceModel, WorkspaceView } from './workspace';
	import type { TrustModel } from '../start/trust';
	import Trust from '../start/Trust.svelte';
	import { failureShort, failureText } from './failure';
	import Folders from '@lucide/svelte/icons/folders';

	// The workspace panel (plan m2b D10, m2c step 10): the tab's project, what the daemon
	// says about it, Trust while it is not, and the way to the projects panel. Every field is
	// a text node.
	interface Props {
		model: WorkspaceModel;
		trust: TrustModel;
		onprojects: () => void;
		/** One line for the toolbar: the name, a short state, long messages in a tooltip. */
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
		<button type="button" class="icon" title="All projects" aria-label="All projects" onclick={onprojects}><Folders size={14} /></button>
		{#if view?.info}
			<strong title="{view.info.name} ({view.info.id}), {view.root}">{view.info.name}</strong>
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
	.icon {
		display: grid;
		place-items: center;
		width: 24px;
		padding: 0;
	}
	.state {
		color: var(--text-2);
		font-size: var(--fs-small);
	}
	.bad {
		color: var(--danger);
		font-size: var(--fs-small);
	}
</style>
