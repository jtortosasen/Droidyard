<script lang="ts">
	import { failureText } from '../build/failure';
	import type { WorkspaceModel, WorkspaceView } from '../build/workspace';
	import type { ProjectFileModel, ProjectFileView } from '../start/projectfile';
	import { gradleChoice } from './gradleroot';

	// The toolbar's Gradle root (ADR-0075 §7), beside the task selector: the root the daemon
	// resolved, a select when there is more than one candidate or no root, the daemon's
	// message when there is nothing. A choice is written to the project file; once that
	// write ends the workspace is described again, and the select shows what the daemon
	// then says, never the choice itself. Every field is a text node.
	interface Props {
		workspace: WorkspaceModel;
		file: ProjectFileModel;
	}
	let { workspace, file }: Props = $props();

	let wsView = $state.raw<WorkspaceView | null>(null);
	let fileView = $state.raw<ProjectFileView | null>(null);
	let busy = $state(false);

	$effect(() => {
		const m = workspace;
		const read = () => {
			wsView = m.view;
		};
		read();
		return m.subscribe(read);
	});
	$effect(() => {
		const m = file;
		const read = () => {
			fileView = m.view;
		};
		read();
		return m.subscribe(read);
	});

	const choice = $derived(gradleChoice(wsView?.gradle ?? null));
	const unsaved = $derived(fileView?.writeError ? failureText(fileView.writeError) : (fileView?.notice ?? null));

	async function choose(el: HTMLSelectElement) {
		busy = true;
		try {
			await file.update({ gradleRoot: el.value });
			await workspace.refresh();
		} finally {
			busy = false;
			// A write that failed or was refused leaves the daemon's root as it was: show that one.
			el.value = choice?.kind === 'select' ? (choice.root ?? '') : el.value;
		}
	}
</script>

{#if choice?.kind === 'fixed'}
	<span class="label" title="The Gradle root, relative to the project root">Gradle: {choice.root}</span>
{:else if choice?.kind === 'select'}
	<label class="label" title={choice.message ?? 'Choose the Gradle root'}>
		Gradle:
		<select
			class="root"
			aria-label="Gradle root"
			disabled={busy}
			value={choice.root ?? ''}
			onchange={(e) => void choose(e.currentTarget)}
		>
			{#if choice.root === undefined}<option value="" disabled>choose a root</option>{/if}
			{#each choice.options as o (o)}
				<option value={o}>{o}</option>
			{/each}
		</select>
	</label>
{:else if choice?.kind === 'none'}
	<span class="bad" title={choice.message}>{choice.message}</span>
{/if}
{#if unsaved}<span class="bad" title={unsaved}>not saved</span>{/if}

<style>
	.label {
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		font-family: var(--font-mono);
		display: inline-flex;
		align-items: center;
		gap: var(--sp-2);
		color: var(--text-2);
		font-size: var(--fs-small);
		white-space: nowrap;
	}
	.bad {
		color: var(--danger);
		font-size: var(--fs-small);
		white-space: nowrap;
	}
</style>
