<script lang="ts">
	import type { BuildModel, BuildView } from './build';
	import Hammer from '@lucide/svelte/icons/hammer';
	import { failureShort, failureText } from './failure';

	// The build panel (plan m2b step 3): module and variant from the daemon, Build, the
	// state of any client's build (the mirror, D11). The last result and its problems are the
	// bottom Problems tab's (BuildResult.svelte, plan m2d step 2).
	// Every field is a text node.
	interface Props {
		model: BuildModel;
		/** The toolbar's hammer (the status is BuildStatus's, module and variant TargetMenu's): errors as `Failed: CODE` with the text in a tooltip, in full in the Problems tab. */
		compact?: boolean;
	}
	let { model, compact = false }: Props = $props();

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
	// Why Build does nothing, or null (the hammer's tooltip).
	const buildReason = $derived.by(() => {
		if (!view) return 'Loading';
		if (building !== null) return 'A build is running';
		if (view.starting) return 'The build is starting';
		if (view.module === '' || view.variant === '') return 'No module and variant chosen';
		return null;
	});

	function progressText(): string {
		if (!progress) return '';
		const count = progress.total ? `${progress.done ?? 0} of ${progress.total}` : `${progress.done ?? 0}`;
		return [progress.phase, count, progress.message].filter((x) => x).join(' ');
	}
</script>

{#if compact}
	<section class="compact">
		{#if view?.open}
			<button
				type="button"
				class="hammer"
				title={buildReason ?? 'Build the chosen module and variant'}
				aria-label="Build"
				disabled={buildReason !== null}
				onclick={() => void model.build()}
			>
				<Hammer size={14} />
			</button>
			{#if building}
				<!-- The toolbar's build status shows the build and Cancel (toolbar/BuildStatus.svelte). -->
			{:else if view.startError}
				<span class="bad" title={failureText(view.startError)}>{failureShort(view.startError)}</span>
			{:else if view.loadError}
				<span class="bad" title={failureText(view.loadError)}>{failureShort(view.loadError)}</span>
			{/if}
		{/if}
	</section>
{:else}
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
	{/if}
</section>
{/if}

<style>
	.compact {
		display: flex;
		align-items: center;
		gap: var(--sp-3);
		white-space: nowrap;
		min-width: 0;
	}
	.hammer {
		flex: none;
		display: grid;
		place-items: center;
		width: 28px;
		padding: 0;
	}
	.bad {
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		color: var(--danger);
		font-size: var(--fs-small);
	}
</style>
