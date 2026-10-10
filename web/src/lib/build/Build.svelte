<script lang="ts">
	import type { BuildModel, BuildView } from './build';
	import { failureText } from './failure';
	import Problems from './Problems.svelte';

	// The build panel (plan m2b step 3): module and variant from the daemon, Build, the
	// state of any client's build (the mirror, D11) and the last result with its problems.
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

	const building = $derived(view?.mirror.building ?? null);
	const progress = $derived(building?.progress);

	function progressText(): string {
		if (!progress) return '';
		const count = progress.total ? `${progress.done ?? 0} of ${progress.total}` : `${progress.done ?? 0}`;
		return [progress.phase, count, progress.message].filter((x) => x).join(' ');
	}
</script>

<section>
	<h2>Build</h2>
	{#if !view}
		<p>Loading...</p>
	{:else if !view.open}
		<p>Open a workspace to build.</p>
	{:else}
		<p>
			<label>
				Module
				<select value={view.module} onchange={(e) => void model.selectModule(e.currentTarget.value)}>
					{#each view.modules as m (m)}
						<option value={m}>{m}</option>
					{/each}
				</select>
			</label>
			<label>
				Variant
				<select value={view.variant} onchange={(e) => model.selectVariant(e.currentTarget.value)}>
					{#each view.variants as v (v)}
						<option value={v}>{v}</option>
					{/each}
				</select>
			</label>
			<button
				type="button"
				disabled={building !== null || view.starting || view.module === '' || view.variant === ''}
				onclick={() => void model.build()}
			>
				Build
			</button>
		</p>
		{#if view.loadError}
			<p>{failureText(view.loadError)}</p>
		{/if}
		{#if view.startError}
			<p>Build refused: {failureText(view.startError)}</p>
		{/if}
		{#if building}
			<p>
				Building ({building.client ?? 'unknown client'}): {building.capability}, {building.state}.
				{#if building.state === 'queued'}
					Queued behind another build.
				{/if}
				{progressText()}
				<button type="button" onclick={() => void model.mirror.cancel()}>Cancel</button>
			</p>
			{#if view.mirror.cancelError}
				<p>Cancel failed: {view.mirror.cancelError}</p>
			{/if}
		{:else}
			<p>Idle.</p>
		{/if}

		<h3>Last build</h3>
		{#if view.noBuild}
			<p>No build yet.</p>
		{:else if view.last}
			<p>
				{view.last.outcome}, {view.last.buildId}
				{view.last.request.module ?? ''} {view.last.request.variant ?? ''}
				({view.last.request.tasks.join(' ')}), {view.last.durationMs ?? ''} ms
			</p>
			{#if view.problems}
				<Problems problems={view.problems} />
			{/if}
		{/if}
	{/if}
</section>
