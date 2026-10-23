<script lang="ts">
	import ChevronDown from '@lucide/svelte/icons/chevron-down';
	import type { BuildModel, BuildView } from '../build/build';
	import { placeBelow } from './popover';

	// The toolbar's module · variant menu (m2e D34): one Popover API popover with both
	// lists, the modules and the chosen module's variants. Every field is a text node.
	interface Props {
		model: BuildModel;
	}
	let { model }: Props = $props();

	let view = $state.raw<BuildView | null>(null);
	let pop = $state<HTMLDivElement>();
	let anchor = $state<HTMLButtonElement>();
	let top = $state(0);
	let left = $state(0);

	$effect(() => {
		const m = model;
		const read = () => {
			view = m.view;
		};
		read();
		return m.subscribe(read);
	});

	const label = $derived(view?.module ? `${view.module} · ${view.variant}` : 'Module · variant');

	function toggle() {
		if (!pop || !anchor) return;
		({ top, left } = placeBelow(anchor, 480)); // its widest (max-width), so it stays inside the window
		pop.togglePopover();
	}
</script>

<div class="select">
	<button
		bind:this={anchor}
		type="button"
		class="trigger"
		disabled={!view?.open}
		title={view?.open ? 'Choose the module and variant' : 'Open a project first'}
		aria-haspopup="menu"
		onclick={toggle}
	>
		<span class="label">{label}</span>
		<ChevronDown size={12} />
	</button>
	<div bind:this={pop} popover="auto" class="pop" style:top="{top}px" style:left="{left}px" aria-label="Module and variant">
		{#if view?.open}
			<div class="cols">
				<div class="col" role="group" aria-label="Module">
					<h3>Module</h3>
					{#each view.modules as m (m)}
						<button type="button" class="item" class:current={m === view.module} aria-pressed={m === view.module} onclick={() => void model.selectModule(m)}>{m}</button>
					{/each}
				</div>
				<div class="col" role="group" aria-label="Variant">
					<h3>Variant</h3>
					{#each view.variants as v (v)}
						<button type="button" class="item" class:current={v === view.variant} aria-pressed={v === view.variant} onclick={() => model.selectVariant(v)}>{v}</button>
					{/each}
				</div>
			</div>
		{/if}
	</div>
</div>

<style>
	.select {
		display: inline-flex;
		min-width: 0;
	}
	.trigger {
		display: inline-flex;
		min-width: 0;
		overflow: hidden;
		font-family: var(--font-mono);
		font-size: var(--fs-small);
		align-items: center;
		gap: var(--sp-3);
		max-width: 220px;
	}
	.label {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.pop {
		position: fixed;
		inset: auto;
		margin: 0;
		width: max-content;
		min-width: 240px;
		max-width: min(480px, calc(100vw - 32px));
		max-height: 60vh;
		overflow: auto;
		padding: var(--sp-3);
		border: 1px solid var(--border);
		border-radius: var(--radius-panel);
		background: var(--overlay-solid);
		color: var(--text);
		box-shadow: var(--shadow);
	}
	.pop:not(:popover-open) {
		display: none;
	}
	.pop:popover-open .cols {
		display: flex;
		align-items: flex-start;
		gap: var(--sp-4);
	}
	.col {
		flex: 1 1 auto;
		display: flex;
		flex-direction: column;
		align-items: stretch;
		gap: var(--sp-1);
	}
	h3 {
		margin: var(--sp-2) var(--sp-3);
		font-size: var(--fs-small);
		font-weight: 600;
		color: var(--text-2);
		text-transform: uppercase;
		letter-spacing: var(--tracking-label);
	}
	.item {
		display: block;
		width: 100%;
		text-align: left;
		border-color: transparent;
		background: transparent;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.item.current {
		background: var(--bg-active);
	}
</style>
