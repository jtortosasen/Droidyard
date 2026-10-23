<script lang="ts">
	import { untrack, type Component } from 'svelte';
	import File from '@lucide/svelte/icons/file';
	import FileCode from '@lucide/svelte/icons/file-code';
	import FileJson from '@lucide/svelte/icons/file-json';
	import FileText from '@lucide/svelte/icons/file-text';
	import Image from '@lucide/svelte/icons/image';
	import X from '@lucide/svelte/icons/x';
	import { fileIcon, type IconKind } from '../files/tree';
	import Viewer from './Viewer.svelte';
	import type { TabsModel, TabsView } from './tabs';
	import type { UnsavedGuard } from './unsaved';
	import type { ViewerSet } from './viewer';
	import type { EditorDiagnostic } from './surface';
	import type { Position } from '../build/locate';

	// The Editor tab's content (plan m2d step 6): the second-level tabs of the open files
	// over a read-only viewer. A single click on a tree file is a preview tab (italics);
	// a double-click on a tab keeps it. Choosing or closing a tab reveals the file in the tree.
	// A tab with unsaved changes has a dot, and closing it asks first (m2i D7).
	interface Props {
		tabs: TabsModel;
		viewers: ViewerSet;
		/** A tab was chosen or the active one changed by closing: the tree reveals the file. */
		onactivate: (path: string) => void;
		copyPath: (path: string) => string;
		/** Asks before a tab with unsaved changes is closed. */
		guard: UnsavedGuard;
		/** The last build's errors by file (plan m2i D10); one array per file and build result. */
		marks: ReadonlyMap<string, EditorDiagnostic[]>;
		/** A Problems row asked for a position; `ontarget(id)` says the editor has moved there. */
		target: (Position & { id: number }) | null;
		ontarget: (id: number) => void;
	}
	let { tabs, viewers, onactivate, copyPath, guard, marks, target, ontarget }: Props = $props();

	const ICONS: Record<IconKind, Component<{ size?: number }>> = {
		folder: File,
		'folder-open': File,
		module: File,
		code: FileCode,
		json: FileJson,
		text: FileText,
		image: Image,
		file: File,
	};

	let view = $state.raw<TabsView>(untrack(() => tabs.view));

	$effect(() => {
		const m = tabs;
		view = m.view;
		return m.subscribe(() => {
			view = m.view;
			viewers.keep(view.tabs.map((t) => t.path));
		});
	});

	const NONE: readonly EditorDiagnostic[] = [];

	const name = (path: string) => path.slice(path.lastIndexOf('/') + 1);

	function choose(path: string) {
		tabs.activate(path);
		onactivate(path);
	}
	function close(path: string) {
		guard.confirm(() => {
			const next = tabs.close(path);
			if (next !== null) onactivate(next);
		}, [path]);
	}
	function onkeydown(e: KeyboardEvent, path: string) {
		if (e.key === 'Enter' || e.key === ' ') {
			e.preventDefault();
			choose(path);
		}
	}
</script>

<div class="editor">
	{#if view.tabs.length === 0}
		<div class="empty">
			<FileCode size={28} />
			<p>Open a file from the tree to read it here.</p>
		</div>
	{:else}
		<div class="strip" role="tablist" aria-label="Open files">
			{#each view.tabs as t (t.path)}
				{@const Icon = ICONS[fileIcon(t.path)]}
				<div class="file" class:current={view.active === t.path} class:preview={t.preview}>
					<button
						type="button"
						role="tab"
						class="label"
						aria-selected={view.active === t.path}
						title={t.preview ? `${t.path} (preview)` : t.path}
						onclick={() => choose(t.path)}
						ondblclick={() => tabs.keep(t.path)}
						onkeydown={(e) => onkeydown(e, t.path)}
					>
						<Icon size={13} />
						<span>{name(t.path)}</span>
						{#if t.dirty}<span class="dirty" role="img" aria-label="unsaved changes" title="Unsaved changes"></span>{/if}
					</button>
					<button type="button" class="close" aria-label="Close {name(t.path)}" title="Close" onclick={() => close(t.path)}><X size={12} /></button>
				</div>
			{/each}
		</div>
		{#if view.active !== null}
			{#key view.active}
				<Viewer
					viewer={viewers.get(view.active)}
					{copyPath}
					diagnostics={marks.get(view.active) ?? NONE}
					at={target?.path === view.active ? target : null}
					{ontarget}
				/>
			{/key}
		{/if}
	{/if}
</div>

<style>
	/* The shell's pane pads and scrolls its content; the editor fills the centre's body
	   (the positioned parent) instead, so the viewer owns its own scrolling. */
	.editor {
		position: absolute;
		inset: 0;
		background: var(--bg-panel);
		display: flex;
		flex-direction: column;
	}
	.empty {
		flex: 1;
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		gap: var(--sp-4);
		color: var(--text-3);
	}
	.empty p {
		margin: 0;
	}
	.strip {
		flex: none;
		display: flex;
		overflow-x: auto;
		background: var(--bg-raised);
		border-bottom: 1px solid var(--border);
	}
	.file {
		flex: none;
		display: inline-flex;
		align-items: center;
		height: var(--ftab-height);
		border-right: 1px solid var(--border);
		border-bottom: 2px solid transparent;
		color: var(--text-2);
		font-size: var(--fs-small);
	}
	.file:hover {
		background: var(--bg-hover);
	}
	.file.current {
		background: var(--bg-panel);
		color: var(--text);
		border-bottom-color: var(--accent);
	}
	.file.preview .label span {
		font-style: italic;
	}
	.dirty {
		flex: none;
		width: 7px;
		height: 7px;
		border-radius: 50%;
		background: var(--text-2);
	}
	.label,
	.close {
		display: inline-flex;
		align-items: center;
		min-height: 0;
		border: 0;
		background: transparent;
		color: inherit;
	}
	.label {
		gap: var(--sp-3);
		height: 100%;
		padding: 0 var(--sp-2) 0 var(--sp-4);
		font-size: inherit;
		white-space: nowrap;
	}
	.close {
		justify-content: center;
		width: 18px;
		height: 18px;
		margin-right: var(--sp-2);
		padding: 0;
		border-radius: var(--radius-control);
		color: var(--text-3);
	}
	.close:hover {
		background: var(--bg-active);
		color: var(--text);
	}
</style>
