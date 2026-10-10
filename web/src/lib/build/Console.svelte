<script lang="ts">
	import { failureText } from './failure';
	import type { ConsoleModel, ConsoleView } from './console';

	// The console of the last build (plan m2b D14), as text in a <pre>: never markup.
	interface Props {
		model: ConsoleModel;
	}
	let { model }: Props = $props();

	let view = $state.raw<ConsoleView | null>(null);

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
	<h2>Console</h2>
	{#if !view}
		<p>Loading...</p>
	{:else if view.building}
		<p>A build is running: the console shows after the build.</p>
	{:else if !view.hasBuild}
		<p>No build console yet.</p>
	{:else}
		<p>
			{#if view.offset !== null}
				From byte {view.offset} of {view.size ?? 'unknown'}{view.growing ? ', still growing' : ''}.
			{/if}
			{#if view.hasEarlier}
				<button type="button" disabled={view.loading} onclick={() => void model.earlier()}>Earlier</button>
			{/if}
			{#if view.loading}
				Loading...
			{/if}
		</p>
		{#if view.error}
			<p>{failureText(view.error)}</p>
		{/if}
		<pre>{view.text}</pre>
	{/if}
</section>

<style>
	pre {
		max-height: 30rem;
		overflow: auto;
		white-space: pre-wrap;
		overflow-wrap: anywhere;
	}
</style>
