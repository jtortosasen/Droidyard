<script lang="ts">
	import ListChecks from '@lucide/svelte/icons/list-checks';
	import Play from '@lucide/svelte/icons/play';
	import { failureText } from '../build/failure';
	import { MAX_TASKS, type TasksModel, type TasksView } from '../build/tasks';
	import { placeBelow } from './popover';
	import { selectionLabel, selectorState } from './selector';

	// The toolbar's task selector (plan m2d step 8): a searchable list over `build.tasks`
	// in a popover (the toolbar clips, the top layer does not) and a run button for
	// `build.run`. Disabled with its reason shown as the tooltip. Every field is a text node.
	interface Props {
		model: TasksModel;
	}
	let { model }: Props = $props();

	let view = $state.raw<TasksView | null>(null);
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

	// A long list is cut for the page; the filter narrows it.
	const SHOWN = 200;
	const st = $derived(selectorState(view));

	function toggle() {
		if (!pop || !anchor) return;
		({ top, left } = placeBelow(anchor, 340));
		pop.togglePopover();
	}
</script>

<div class="select">
	<button
		bind:this={anchor}
		type="button"
		class="trigger"
		disabled={st.disabled}
		title={st.reason ?? `Tasks: ${selectionLabel(view?.selected ?? [])}`}
		aria-label="Tasks"
		aria-haspopup="dialog"
		onclick={toggle}
	>
		<ListChecks size={14} />
		{#if view && view.selected.length > 0}<span class="count">{view.selected.length}</span>{/if}
	</button>
	{#if view?.runError}
		<span class="bad" title={failureText(view.runError)}>{view.runError.code ?? 'Run refused'}</span>
	{/if}

	<div bind:this={pop} popover="auto" class="pop" style:top="{top}px" style:left="{left}px">
		{#if view && view.open && view.trusted}
			<input
				type="search"
				placeholder="Filter tasks"
				aria-label="Filter tasks"
				value={view.filter}
				oninput={(e) => model.setFilter(e.currentTarget.value)}
			/>
			<div class="bar">
				<span>{view.selected.length} of {MAX_TASKS} chosen</span>
				<button
					type="button"
					class="run"
					disabled={st.runDisabled}
					title={st.runReason ?? 'Run the chosen tasks'}
					aria-label="Run the chosen tasks"
					onclick={() => void model.run()}
				>
					<Play size={12} /> Run
				</button>
				<button type="button" disabled={view.loading} onclick={() => void model.load()}>Refresh</button>
				<button type="button" disabled={view.selected.length === 0} onclick={() => model.clearSelection()}>Clear</button>
			</div>
			{#if view.loadError}<p class="bad">{failureText(view.loadError)}</p>{/if}
			{#if view.stale}<p class="note">The task list may be out of date.</p>{/if}
			{#if view.limitReached}<p class="note">At most {MAX_TASKS} tasks per run: unchoose one first.</p>{/if}
			<ul>
				{#each view.filtered.slice(0, SHOWN) as t (t.path)}
					<li>
						<label title={t.description ?? ''}>
							<input type="checkbox" checked={view.selected.includes(t.path)} onchange={() => model.toggle(t.path)} />
							<span class="path">{t.path}</span>
						</label>
					</li>
				{/each}
			</ul>
			{#if view.filtered.length > SHOWN}
				<p class="note">{view.filtered.length - SHOWN} more tasks: narrow the filter.</p>
			{:else if view.loading}
				<p class="note">Loading tasks...</p>
			{:else if view.filtered.length === 0}
				<p class="note">{view.tasks.length === 0 ? 'No tasks listed.' : 'No task matches the filter.'}</p>
			{/if}
		{/if}
	</div>
</div>

<style>
	.select {
		display: inline-flex;
		align-items: center;
		gap: var(--sp-2);
	}
	.trigger {
		display: inline-flex;
		align-items: center;
		gap: var(--sp-2);
	}
	.count {
		font-size: var(--fs-small);
		color: var(--accent);
	}
	.run {
		display: inline-flex;
		align-items: center;
		gap: var(--sp-2);
		color: var(--success);
	}
	.run:disabled {
		color: var(--text-3);
	}
	.note {
		margin: 0;
		color: var(--text-3);
		font-size: var(--fs-small);
	}
	.bad {
		margin: 0;
		color: var(--danger);
		font-size: var(--fs-small);
	}
	.pop {
		position: fixed;
		inset: auto;
		margin: 0;
		width: 340px;
		max-height: 60vh;
		overflow: auto;
		padding: var(--sp-4);
		border: 1px solid var(--border);
		border-radius: var(--radius-panel);
		background: var(--overlay-solid);
		color: var(--text);
		box-shadow: var(--shadow);
	}
	/* The toolbar's `div` rule would show it closed: say it. */
	.pop:not(:popover-open) {
		display: none;
	}
	.pop:popover-open {
		display: flex;
		flex-direction: column;
		gap: var(--sp-3);
	}
	.pop input[type='search'] {
		width: 100%;
	}
	.bar {
		display: flex;
		align-items: center;
		gap: var(--sp-4);
		color: var(--text-2);
		font-size: var(--fs-small);
	}
	.bar span {
		flex: 1;
	}
	ul {
		margin: 0;
		padding: 0;
		list-style: none;
	}
	label {
		display: flex;
		align-items: center;
		gap: var(--sp-3);
		min-height: var(--row);
	}
	.path {
		font-family: var(--font-mono);
		font-size: var(--fs-mono);
		overflow-wrap: anywhere;
	}
</style>
