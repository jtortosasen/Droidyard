<script lang="ts">
	import Square from '@lucide/svelte/icons/square';
	import type { BuildModel, BuildView } from '../build/build';
	import type { SessionsModel } from '../projects/sessions';
	import Spinner from '../projects/Spinner.svelte';
	import { buildStatus } from './status';

	// The build status, compact, opening the toolbar's right-aligned group (plan m2d step 8, m2e D34): any client's build of this
	// workspace, from the build mirror, with Cancel. Every field is a text node.
	interface Props {
		build: BuildModel;
		sessions: SessionsModel;
	}
	let { build, sessions }: Props = $props();

	let building = $state.raw<BuildView['mirror']['building']>(null);
	let cancelError = $state.raw<string | null>(null);
	let labels = $state.raw<ReadonlyMap<string, string>>(new Map());
	let now = $state(Date.now());

	$effect(() => {
		const m = build;
		const read = () => {
			building = m.view.mirror.building;
			cancelError = m.view.mirror.cancelError;
		};
		read();
		return m.subscribe(read);
	});
	$effect(() => {
		const s = sessions;
		const read = () => {
			labels = s.actorLabels();
		};
		read();
		return s.subscribe(read);
	});
	$effect(() => {
		if (building === null) return;
		now = Date.now();
		const timer = setInterval(() => (now = Date.now()), 1000);
		return () => clearInterval(timer);
	});

	const status = $derived(buildStatus(building, { now, labels }));
</script>

<div class="status" aria-live="polite">
	{#if status}
		<Spinner />
		<span class="text" title={status.title}>{status.text}</span>
		{#if status.actor}<span class="actor">{status.actor}</span>{/if}
		<button type="button" class="stop" title="Cancel the build" aria-label="Cancel the build" onclick={() => void build.mirror.cancel()}>
			<Square size={12} />
		</button>
		{#if cancelError}<span class="bad" title={cancelError}>Cancel failed</span>{/if}
	{:else}
		<span class="idle">Idle</span>
	{/if}
</div>

<style>
	.status {
		display: inline-flex;
		align-items: center;
		gap: var(--sp-4);
		padding: 0 var(--sp-5);
		height: calc(var(--row) + 2px);
		border-radius: var(--radius-control);
		font-size: var(--fs-small);
		white-space: nowrap;
		justify-content: flex-end;
	}
	.text {
		color: var(--text);
		font-variant-numeric: tabular-nums;
	}
	.idle {
		color: var(--text-3);
	}
	.actor {
		padding: 0 var(--sp-3);
		border-radius: 999px;
		background: var(--accent-soft);
		color: var(--accent);
	}
	.stop {
		display: grid;
		place-items: center;
		min-height: 0;
		padding: 0 var(--sp-2);
		border: 0;
		background: transparent;
		color: var(--text-2);
	}
	.bad {
		color: var(--danger);
	}
</style>
