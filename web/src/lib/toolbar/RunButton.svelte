<script lang="ts">
	import Play from '@lucide/svelte/icons/play';
	import Square from '@lucide/svelte/icons/square';
	import { failureText } from '../build/failure';
	import type { RunModel, RunView } from './run';

	// ▶ / ■ over RunModel (m2e D37): ▶ runs, ■ replaces it while a run is in progress and
	// cancels the step's operation. Disabled with its reason as the tooltip. Every field is
	// a text node.
	interface Props {
		model: RunModel;
	}
	let { model }: Props = $props();

	let view = $state.raw<RunView | null>(null);

	$effect(() => {
		const m = model;
		const read = () => {
			view = m.view;
		};
		read();
		return m.subscribe(read);
	});

	const result = $derived.by(() => {
		if (!view) return null;
		if (view.failed) return `${view.failed.step} failed: ${failureText(view.failed.failure)}`;
		if (view.state === 'build-failed') return 'Build failed: see Problems';
		if (view.state === 'cancelled') return 'Run cancelled';
		return null;
	});
</script>

{#if view?.running}
	<button
		type="button"
		class="stop"
		disabled={!view.cancellable}
		title={view.cancellable ? 'Cancel the run' : 'Launching: a launch is not cancelled'}
		aria-label="Cancel the run"
		onclick={() => model.cancel()}
	>
		<Square size={14} />
	</button>
{:else}
	<button
		type="button"
		class="go btn-primary"
		disabled={view === null || view.disabled !== null}
		title={view?.disabled ?? 'Run: start the device if needed, build, install and launch'}
		aria-label="Run"
		onclick={() => void model.run()}
	>
		<Play size={14} />
	</button>
{/if}
{#if result}<span class="bad" title={result}>{result}</span>{/if}

<style>
	.go,
	.stop {
		display: grid;
		place-items: center;
		width: 28px;
		padding: 0;
		flex: none;
	}
	.stop {
		color: var(--danger);
	}
	.stop:disabled {
		color: var(--text-3);
	}
	.bad {
		max-width: 220px;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		color: var(--danger);
		font-size: var(--fs-small);
	}
</style>
