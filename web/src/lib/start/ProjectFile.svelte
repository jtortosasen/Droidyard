<script lang="ts">
	import type { ProjectFileModel, ProjectFileView } from './projectfile';
	import { failureText } from '../build/failure';

	// The project file's notices (ADR-0046): a file that is left alone says why, and a
	// write that failed says so while the choice stays for the tab. Every field is a
	// text node.
	interface Props {
		model: ProjectFileModel;
	}
	let { model }: Props = $props();

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

{#if view?.notice}
	<p>{view.notice}</p>
{/if}
{#if view?.writeError}
	<p>The project file could not be written; your choices last for this tab. {failureText(view.writeError)}</p>
{/if}
