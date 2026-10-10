<script lang="ts">
	import type { WorkspaceModel, WorkspaceView } from './workspace';
	import { failureText } from './failure';

	// The workspace panel (plan m2b D10): the typed root, Open, and what the daemon says
	// about it. Every field is a text node.
	interface Props {
		model: WorkspaceModel;
	}
	let { model }: Props = $props();

	let view = $state.raw<WorkspaceView | null>(null);
	let typed = $state('');

	$effect(() => {
		const m = model;
		typed = m.root; // the root kept from an earlier visit
		const read = () => {
			view = m.view;
		};
		read();
		return m.subscribe(read);
	});

	function open(e: SubmitEvent) {
		e.preventDefault();
		model.setRoot(typed);
		void model.open();
	}
</script>

<section>
	<h2>Workspace</h2>
	<form onsubmit={open}>
		<label>
			Project root
			<input type="text" size="60" bind:value={typed} />
		</label>
		<button type="submit" disabled={typed.trim() === '' || !!view?.opening}>
			{view?.closedByOther ? 'Reopen' : 'Open'}
		</button>
	</form>
	{#if view?.opening}
		<p>Opening...</p>
	{/if}
	{#if view?.error}
		<p>{failureText(view.error)}</p>
	{/if}
	{#if view?.closedByOther}
		<p>This workspace was closed by another client.</p>
	{/if}
	{#if view?.info}
		<p>
			{view.info.name} ({view.info.id}): {view.info.state}, {view.info.trusted ? 'trusted' : 'not trusted'}
		</p>
		{#if view.info.degradedBy}
			<p>Degraded: {failureText(view.info.degradedBy)}</p>
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
