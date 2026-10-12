<script lang="ts">
	import { untrack } from 'svelte';
	import type { Attachment } from 'svelte/attachments';
	import FileQuestion from '@lucide/svelte/icons/file-question';
	import { copyToClipboard } from '../devices/copy';
	import { LazyEditor } from './surface';
	import { formatSize, readOnlyText, CAP, type EditorModel, type ViewerView } from './viewer';

	// The editor of one file (plan m2d step 6, m2i steps 3 and 6): the header, banners and the
	// binary placeholder are here; the text is shown by the code editor behind surface.ts,
	// mounted by an attachment once the model has a buffer, so its chunk loads when a file
	// opens. The buffer lives in the model, not here: switching tabs destroys this component
	// and keeps the document, selection and undo history.
	interface Props {
		viewer: EditorModel;
		/** What Copy path copies (the tree's absolute path). */
		copyPath: (path: string) => string;
	}
	let { viewer, copyPath }: Props = $props();

	let view = $state.raw<ViewerView>(untrack(() => viewer.view));
	let copied = $state('');
	let editorError = $state('');
	const buffer = $derived(view.buffer);

	$effect(() => {
		const v = viewer;
		view = v.view;
		copied = '';
		return v.subscribe(() => {
			view = v.view;
		});
	});

	// Mounts the editor once on its host for the buffer on show; the host goes (and the editor
	// is destroyed) when the buffer does.
	const editor: Attachment<HTMLElement> = (host) => {
		editorError = '';
		const b = untrack(() => buffer);
		if (b === null) return;
		const ed = new LazyEditor(host, { buffer: b, onSave: () => void viewer.save() }, (e) => {
			editorError = e instanceof Error ? e.message : String(e);
		});
		return () => ed.destroy();
	};

	async function copy() {
		const r = await copyToClipboard(copyPath(view.path), typeof navigator === 'undefined' ? undefined : navigator.clipboard);
		copied = r.ok ? 'Copied' : r.reason;
	}
</script>

<div class="viewer">
	<div class="head">
		<span class="path" title={view.path}>{view.path}</span>
		{#if view.saving}<span class="size">saving…</span>{/if}
		{#if view.readOnly}<span class="size">read-only</span>{/if}
		{#if view.size !== null}<span class="size">{formatSize(view.size)}</span>{/if}
	</div>
	{#if view.state === 'deleted'}
		<p class="banner" role="status">This file was deleted from the project.{buffer ? ' The text below is the last version read.' : ''}</p>
	{/if}
	{#if view.state === 'binary'}
		<div class="placeholder">
			<FileQuestion size={28} />
			<p>binary file · {view.size === null ? '' : formatSize(view.size)}</p>
			<button type="button" onclick={copy}>Copy path</button>
			{#if copied}<p class="note" role="status">{copied}</p>{/if}
		</div>
	{:else if view.state === 'error'}
		<p class="banner error" role="alert">{view.error}</p>
	{:else if view.state === 'loading'}
		<p class="note pad">Reading…</p>
	{:else}
		{#if view.readOnly && view.readOnly !== 'capped'}
			<p class="banner" role="status">{readOnlyText(view.readOnly)}</p>
		{/if}
		{#if buffer}
			{#key buffer}
				<div class="host" {@attach editor}></div>
			{/key}
		{/if}
		{#if editorError}
			<p class="banner error" role="alert">The editor could not be loaded: {editorError}</p>
		{/if}
		{#if view.saveError}
			<p class="banner error" role="alert">
				Not saved: {view.conflict ? 'the file changed on disk after it was read, so nothing was written. ' : ''}{view.saveError}
			</p>
		{/if}
		{#if view.capped}
			<p class="banner" role="status">
				Showing the first {formatSize(view.read)} of {view.size === null ? '' : formatSize(view.size)}; the editor reads at most {formatSize(CAP)}.
			</p>
		{/if}
	{/if}
</div>

<style>
	.viewer {
		display: flex;
		flex-direction: column;
		flex: 1;
		min-height: 0;
		min-width: 0;
	}
	.head {
		flex: none;
		display: flex;
		align-items: baseline;
		gap: var(--sp-5);
		padding: var(--sp-2) var(--sp-5);
		border-bottom: 1px solid var(--border);
		font-size: var(--fs-small);
		color: var(--text-2);
	}
	.path {
		flex: 1;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.size {
		flex: none;
		color: var(--text-3);
	}
	/* The editor's host: a flex column its shadow root's editor fills. */
	.host {
		flex: 1;
		min-height: 0;
		min-width: 0;
		display: flex;
		flex-direction: column;
	}
	.banner {
		flex: none;
		margin: 0;
		padding: var(--sp-3) var(--sp-5);
		font-size: var(--fs-small);
		color: var(--warning);
		background: var(--bg-raised);
		border-bottom: 1px solid var(--border);
	}
	.banner.error {
		color: var(--danger);
	}
	.placeholder {
		flex: 1;
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		gap: var(--sp-4);
		color: var(--text-3);
	}
	.placeholder p {
		margin: 0;
	}
	.note {
		margin: 0;
		font-size: var(--fs-small);
		color: var(--text-3);
	}
	.pad {
		padding: var(--sp-5);
	}
</style>
