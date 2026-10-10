<script lang="ts">
	import { failureText } from './failure';
	import { MAX_TASKS, type TasksModel, type TasksView } from './tasks';

	// The Gradle tasks panel (plan m2b step 4): the daemon's task list, a filter, a
	// selection of up to 20 and Run. Building and Cancel come from the build mirror, the
	// same state the build panel shows. Every field is a text node.
	interface Props {
		model: TasksModel;
	}
	let { model }: Props = $props();

	let view = $state.raw<TasksView | null>(null);

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

	const building = $derived(view?.mirror.building ?? null);
</script>

<section>
	<h2>Gradle tasks</h2>
	{#if !view}
		<p>Loading...</p>
	{:else if !view.open}
		<p>Open a workspace to list its tasks.</p>
	{:else}
		<p>
			<label>
				Filter
				<input type="text" value={view.filter} oninput={(e) => model.setFilter(e.currentTarget.value)} />
			</label>
			<button type="button" disabled={view.loading} onclick={() => void model.load()}>Refresh</button>
			<button type="button" disabled={view.selected.length === 0} onclick={() => model.clearSelection()}>Clear</button>
			<button
				type="button"
				disabled={building !== null || view.starting || view.selected.length === 0}
				onclick={() => void model.run()}
			>
				Run
			</button>
			{view.selected.length} of {MAX_TASKS} selected
		</p>
		{#if view.loading}
			<p>Loading tasks...</p>
		{/if}
		{#if view.loadError}
			<p>{failureText(view.loadError)}</p>
		{/if}
		{#if view.stale}
			<p>The task list may be out of date.</p>
		{/if}
		{#if view.limitReached}
			<p>At most {MAX_TASKS} tasks per run: unselect one first.</p>
		{/if}
		{#if view.runError}
			<p>Run refused: {failureText(view.runError)}</p>
		{/if}
		{#if building}
			<p>
				Building ({building.client ?? 'unknown client'}): {building.capability}, {building.state}.
				<button type="button" onclick={() => void model.mirror.cancel()}>Cancel</button>
			</p>
			{#if view.mirror.cancelError}
				<p>Cancel failed: {view.mirror.cancelError}</p>
			{/if}
		{/if}
		<ul>
			{#each view.filtered.slice(0, SHOWN) as t (t.path)}
				<li>
					<label>
						<input
							type="checkbox"
							checked={view.selected.includes(t.path)}
							onchange={() => model.toggle(t.path)}
						/>
						{t.path}
						{t.group ?? ''}
						{t.description ?? ''}
					</label>
				</li>
			{/each}
		</ul>
		{#if view.filtered.length > SHOWN}
			<p>{view.filtered.length - SHOWN} more tasks: narrow the filter.</p>
		{:else if view.tasks.length > 0 && view.filtered.length === 0}
			<p>No task matches the filter.</p>
		{/if}
	{/if}
</section>
