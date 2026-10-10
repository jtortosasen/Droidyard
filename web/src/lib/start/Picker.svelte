<script lang="ts">
	import type { PickerModel, PickerView } from './picker';
	import { failureText } from '../build/failure';

	// The project picker (ADR-0045): the projects this installation has known, live, a click
	// to open one, "Open folder..." where the daemon can raise a dialog and a typed root
	// where it cannot. Every field is a text node.
	interface Props {
		model: PickerModel;
	}
	let { model }: Props = $props();

	let view = $state.raw<PickerView | null>(null);
	let typed = $state('');

	$effect(() => {
		const m = model;
		const read = () => {
			view = m.view;
		};
		read();
		const off = m.subscribe(read);
		m.start();
		return () => {
			off();
			m.stop();
		};
	});

	function openTyped(e: SubmitEvent) {
		e.preventDefault();
		void model.openRoot(typed);
	}
</script>

<section>
	<h2>Projects</h2>
	{#if view?.error}
		<p>{failureText(view.error)}</p>
	{/if}

	{#if view?.chooser === 'available'}
		<button type="button" disabled={view.choosing || view.opening} onclick={() => void model.chooseFolder()}>
			Open folder...
		</button>
		{#if view.choosing}
			<p>Waiting for the folder dialog on the daemon's machine...</p>
		{/if}
	{:else}
		<form onsubmit={openTyped}>
			<label>
				Project root
				<input type="text" size="60" bind:value={typed} />
			</label>
			<button type="submit" disabled={typed.trim() === '' || !!view?.opening}>Open</button>
		</form>
		{#if view?.chooser === 'unavailable' && view.chooserReason}
			<p>No folder dialog: {view.chooserReason}</p>
		{/if}
	{/if}
	{#if view?.opening}
		<p>Opening...</p>
	{/if}

	{#if view && view.watch.state !== 'watching' && view.watch.state !== 'starting' && view.watch.state !== 'idle'}
		<p>Project list: {view.watch.state} {view.watch.detail}</p>
	{/if}

	{#if view && view.rows.length === 0}
		<p>No projects yet.</p>
	{:else if view}
		<ul>
			{#each view.rows as r (r.id)}
				<li>
					<button type="button" disabled={view.opening} onclick={() => void model.openRow(r.id)}>
						{r.name}
					</button>
					{r.root}, last opened {r.lastOpenedAt}, {r.open ? `open (${r.state})` : 'not open'}, {r.trusted
						? 'trusted'
						: 'not trusted'}
				</li>
			{/each}
		</ul>
		{#if view.truncated}
			<p>Only the 50 most recent projects are listed.</p>
		{/if}
	{/if}
</section>
