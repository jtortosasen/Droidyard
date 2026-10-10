<script lang="ts">
	import type { WorkspaceModel, WorkspaceView } from './workspace';
	import type { TrustModel } from '../start/trust';
	import Trust from '../start/Trust.svelte';
	import { failureText } from './failure';

	// The workspace panel (plan m2b D10, m2c step 10): the tab's project, what the daemon
	// says about it, Trust while it is not, and the way back to the picker. Every field is
	// a text node.
	interface Props {
		model: WorkspaceModel;
		trust: TrustModel;
		onpicker: () => void;
	}
	let { model, trust, onpicker }: Props = $props();

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

<section>
	<h2>Workspace</h2>
	<button type="button" onclick={onpicker}>All projects</button>
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
