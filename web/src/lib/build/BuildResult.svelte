<script lang="ts">
	import type { BuildModel, BuildView } from './build';
	import Problems from './Problems.svelte';
	import { failureText } from './failure';

	// The last build's result and its problems, the bottom Problems tab (plan m2d step 2):
	// what the build panel showed under "Last build" before the redesign, unchanged.
	// Every field is a text node.
	interface Props {
		model: BuildModel;
	}
	let { model }: Props = $props();

	let view = $state.raw<BuildView | null>(null);

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
	<h2>Problems</h2>
	{#if view?.startError}
		<p>Build refused: {failureText(view.startError)}</p>
	{/if}
	{#if view?.loadError}
		<p>{failureText(view.loadError)}</p>
	{/if}
	{#if view?.noBuild}
		<p>No build yet.</p>
	{:else if view?.last}
		<p>
			{view.last.outcome}, {view.last.buildId}
			{view.last.request.module ?? ''} {view.last.request.variant ?? ''}
			({view.last.request.tasks.join(' ')}), {view.last.durationMs ?? ''} ms
		</p>
		{#if view.problems}
			<Problems problems={view.problems} />
		{/if}
	{/if}
</section>
