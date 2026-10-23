<script lang="ts">
	import type { AppLogsModel, AppLogsView } from './applogs';
	import { failureText } from '../build/failure';

	// The app's logs since its launch (plan m2b step 6): the crash summary first, then the
	// lines. Everything the device wrote is a text node in a <pre>: never markup.
	interface Props {
		model: AppLogsModel;
		connected: boolean;
	}
	let { model, connected }: Props = $props();

	let view = $state.raw<AppLogsView | null>(null);

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
	<h2>App logs</h2>
	{#if !view}
		<p>Loading...</p>
	{:else}
		<p>
			<label>
				Application id
				<input type="text" value={view.applicationId} oninput={(e) => model.setApplicationId(e.currentTarget.value)} />
			</label>
			<button
				type="button"
				disabled={!connected || view.device === '' || view.applicationId.trim() === '' || view.loading}
				onclick={() => void model.load()}
			>
				Read the logs
			</button>
			{#if view.device === ''}
				Select a device first.
			{:else if view.loading}
				Reading...
			{/if}
		</p>
		<div class="scroll">
			{#if view.error}
				<p>device.logs failed: {failureText(view.error)}</p>
			{/if}
			{#each view.notes as note, i (i)}
				<p>{note}</p>
			{/each}
			{#if view.crashes.length > 0}
				<h3>Crashes</h3>
				{#each view.crashes as crash, i (i)}
					<pre>{crash.lines.join('\n')}</pre>
				{/each}
			{/if}
			{#if view.lines.length > 0}
				<h3>Lines</h3>
				<pre class="lines">{view.lines.join('\n')}</pre>
			{/if}
		</div>
	{/if}
</section>

<style>
	section {
		flex: 1;
		min-height: 0;
		display: flex;
		flex-direction: column;
	}
	section > p {
		flex: none;
	}
	.scroll {
		flex: 1;
		min-height: 54px;
		overflow: auto;
		overscroll-behavior: contain;
	}
	pre {
		font-size: var(--fs-log);
		white-space: pre-wrap;
		overflow-wrap: anywhere;
	}
</style>
