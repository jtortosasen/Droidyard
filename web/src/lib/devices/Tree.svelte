<script lang="ts">
	import type { UiTreeModel, UiTreeView } from './uitree';
	import { failureText } from '../build/failure';

	// The UI tree of the selected device (plan m2b step 6). Every node field is a text
	// node in a cell: never markup, never a link, never a class or style. Pick hands the
	// row's position to the parent, which asks the model for the selector.
	interface Props {
		model: UiTreeModel;
		onpick: (index: number) => void;
		connected: boolean;
	}
	let { model, onpick, connected }: Props = $props();

	let view = $state.raw<UiTreeView | null>(null);

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
	<h2>UI tree</h2>
	{#if !view}
		<p>Loading...</p>
	{:else}
		<p>
			<button type="button" disabled={!connected || view.device === '' || view.loading} onclick={() => void model.load()}>
				Read the tree
			</button>
			{#if view.device === ''}
				Select a device first.
			{:else if view.loading}
				Reading...
			{/if}
		</p>
		{#if view.error}
			<p>device.uiTree failed: {failureText(view.error)}</p>
		{/if}
		{#each view.notes as note, i (i)}
			<p>{note}</p>
		{/each}
		{#if view.rows.length > 0}
			<table>
				<thead>
					<tr>
						<th></th>
						<th>Class</th>
						<th>Id</th>
						<th>Text</th>
						<th>Desc</th>
						<th>Flags</th>
					</tr>
				</thead>
				<tbody>
					{#each view.rows as row (row.index)}
						<tr>
							<td><button type="button" onclick={() => onpick(row.index)}>Pick</button></td>
							<td class="node">{row.indent}{row.class}</td>
							<td>{row.id}</td>
							<td>{row.text}</td>
							<td>{row.desc}</td>
							<td>{row.flags}</td>
						</tr>
					{/each}
				</tbody>
			</table>
		{:else if view.loaded && !view.error}
			<p>The tree is empty.</p>
		{/if}
	{/if}
</section>

<style>
	table {
		border-collapse: collapse;
	}
	th,
	td {
		text-align: left;
		padding: 0 0.75rem 0 0;
		font-family: ui-monospace, monospace;
		font-size: 0.85rem;
		overflow-wrap: anywhere;
	}
	.node {
		white-space: pre;
	}
</style>
