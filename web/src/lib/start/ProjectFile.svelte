<script lang="ts">
	import type { ProjectFileModel, ProjectFileView } from './projectfile';
	import { failureText } from '../build/failure';
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import FileText from '@lucide/svelte/icons/file-text';

	const writeText = (v: ProjectFileView) => `The project file could not be written; your choices last for this tab. ${v.writeError ? failureText(v.writeError) : ''}`;

	// The project file's notices (ADR-0046): a file that is left alone says why, and a
	// write that failed says so while the choice stays for the tab. Every field is a
	// text node.
	interface Props {
		model: ProjectFileModel;
		/** An indicator for the toolbar: an icon, the text in its tooltip. */
		compact?: boolean;
	}
	let { model, compact = false }: Props = $props();

	let view = $state.raw<ProjectFileView | null>(null);

	$effect(() => {
		const m = model;
		const read = () => {
			view = m.view;
		};
		read();
		return m.subscribe(read);
	});
</script>

{#if compact}
	{#if view?.writeError}
		<span class="indicator bad" role="img" aria-label={writeText(view)} title={writeText(view)}><TriangleAlert size={14} /></span>
	{:else if view?.notice}
		<span class="indicator" role="img" aria-label={view.notice} title={view.notice}><FileText size={14} /></span>
	{/if}
{:else}
	{#if view?.notice}
		<p>{view.notice}</p>
	{/if}
	{#if view?.writeError}
		<p>{writeText(view)}</p>
	{/if}
{/if}

<style>
	.indicator {
		display: inline-flex;
		color: var(--text-2);
	}
	.indicator.bad {
		color: var(--danger);
	}
</style>
