<script lang="ts">
	import { tick, untrack, type Component } from 'svelte';
	import ChevronRight from '@lucide/svelte/icons/chevron-right';
	import Eye from '@lucide/svelte/icons/eye';
	import EyeOff from '@lucide/svelte/icons/eye-off';
	import File from '@lucide/svelte/icons/file';
	import FileCode from '@lucide/svelte/icons/file-code';
	import FileJson from '@lucide/svelte/icons/file-json';
	import FileText from '@lucide/svelte/icons/file-text';
	import Folder from '@lucide/svelte/icons/folder';
	import FolderOpen from '@lucide/svelte/icons/folder-open';
	import Image from '@lucide/svelte/icons/image';
	import Package from '@lucide/svelte/icons/package';
	import Search from '@lucide/svelte/icons/search';
	import X from '@lucide/svelte/icons/x';
	import { copyToClipboard } from '../devices/copy';
	import { rowWindow } from '../logcat/virtual';
	import { nameOf, parentOf, type FileOps, refuseName } from './ops';
	import type { FileWatch } from './watch';
	import type { FileTreeModel, IconKind, Row, TreeView } from './tree';

	// The file tree (plan m2d step 5): a source list over the model's rows, drawn on 2a's
	// fixed-height virtual list. The model keeps the folders and the selection; this
	// component draws the rows in view and reports what the user did. Names are text nodes,
	// never markup. The watch ends while the page is hidden (D26).
	interface Props {
		model: FileTreeModel;
		watch: FileWatch;
		/** A file was chosen (a click, Enter). */
		onopen: (path: string) => void;
		/** The file operations (plan m2g step 3). */
		ops: FileOps;
		/** An entry was renamed: the open tabs follow it (D57). */
		onmoved: (from: string, to: string) => void;
	}
	let { model, watch, onopen, ops, onmoved }: Props = $props();

	const OVERSCAN = 8;
	const ICONS: Record<IconKind, Component<{ size?: number }>> = {
		folder: Folder,
		'folder-open': FolderOpen,
		module: Package,
		code: FileCode,
		json: FileJson,
		text: FileText,
		image: Image,
		file: File,
	};

	let view = $state.raw<TreeView>(untrack(() => model.view));
	let scroller = $state<HTMLDivElement>();
	let scrollTop = $state(0);
	let viewportHeight = $state(300);
	let rowHeight = $state(22);
	let menu = $state<{ x: number; y: number; path: string } | null>(null);
	let copied = $state('');
	// The inline name field (D58): a new file or folder under `parent`, or the rename of `path`.
	let edit = $state<{ mode: 'file' | 'dir' | 'rename'; parent: string; path: string; value: string; error: string } | null>(null);
	let busy = false;
	let revealSeen = 0;

	const win = $derived(
		rowWindow({ scrollTop, viewportHeight, rowHeight, overscan: OVERSCAN, count: view.rows.length, following: false }),
	);
	const shown = $derived(view.rows.slice(win.start, win.end));
	const selectedIndex = $derived(view.selected === null ? -1 : view.rows.findIndex((r) => r.path === view.selected));

	function measure() {
		if (!scroller) return;
		const px = parseFloat(getComputedStyle(scroller).getPropertyValue('--row'));
		if (px > 0) rowHeight = px;
	}

	async function read(m: FileTreeModel) {
		view = m.view;
		measure();
		if (view.revealSeq !== revealSeen) {
			revealSeen = view.revealSeq;
			await tick();
			scrollToSelected();
		}
	}

	$effect(() => {
		const m = model;
		untrack(() => void read(m));
		return m.subscribe(() => void read(m));
	});

	function scrollToSelected() {
		if (!scroller || selectedIndex < 0) return;
		const top = selectedIndex * rowHeight;
		let next = scrollTop;
		if (top < scrollTop) next = top;
		else if (top + rowHeight > scrollTop + viewportHeight) next = top + rowHeight - viewportHeight;
		if (next !== scrollTop) {
			scroller.scrollTop = next;
			scrollTop = next;
		}
	}

	function onscroll() {
		if (scroller) scrollTop = scroller.scrollTop;
	}

	function choose(row: Row) {
		model.select(row.path);
		if (row.dir) void model.toggle(row.path);
		else onopen(row.path);
	}

	function onkeydown(e: KeyboardEvent) {
		if (e.key === 'ContextMenu' || (e.key === 'F10' && e.shiftKey)) {
			e.preventDefault();
			const row = view.rows[selectedIndex];
			if (row && scroller) {
				const box = scroller.getBoundingClientRect();
				openMenu(row.path, box.left + 24, box.top + (selectedIndex + 1) * rowHeight - scrollTop);
			}
			return;
		}
		if (e.key === 'F2') {
			e.preventDefault();
			const row = view.rows[selectedIndex];
			if (row) beginRename(row.path);
			return;
		}
		if (e.metaKey || e.ctrlKey || e.altKey) return;
		const r = model.key(e.key);
		if (!r.handled) return;
		e.preventDefault();
		if (r.open !== undefined) onopen(r.open);
	}

	function openMenu(path: string, x: number, y: number) {
		copied = '';
		model.select(path);
		menu = { x, y, path };
		void tick().then(() => document.querySelector<HTMLElement>('.menu button')?.focus());
	}

	function oncontextmenu(e: MouseEvent, row: Row) {
		e.preventDefault();
		openMenu(row.path, e.clientX, e.clientY);
	}

	async function copy(text: string) {
		const r = await copyToClipboard(text, typeof navigator === 'undefined' ? undefined : navigator.clipboard);
		copied = r.ok ? '' : r.reason;
		if (r.ok) closeMenu();
	}

	async function beginCreate(mode: 'file' | 'dir', at: string) {
		const row = view.rows.find((r) => r.path === at);
		const parent = row === undefined || row.dir ? at : parentOf(at);
		closeMenu();
		if (parent !== '') {
			await model.reveal(parent);
			await model.expand(parent);
		}
		edit = { mode, parent, path: '', value: '', error: '' };
	}

	function beginRename(path: string) {
		closeMenu();
		model.select(path);
		edit = { mode: 'rename', parent: parentOf(path), path, value: nameOf(path), error: '' };
	}

	function cancelEdit() {
		edit = null;
		scroller?.focus();
	}

	async function commit(fromBlur: boolean) {
		const cur = edit;
		if (cur === null || busy) return;
		if (fromBlur && (cur.value === '' || (cur.mode === 'rename' && cur.value === nameOf(cur.path)))) {
			edit = null;
			return;
		}
		const why = refuseName(cur.value);
		if (why !== null) {
			cur.error = why;
			return;
		}
		busy = true;
		try {
			if (cur.mode === 'rename') {
				const r = await ops.rename(cur.path, cur.value);
				if (edit !== cur) return;
				if (!r.ok) {
					cur.error = r.message;
					return;
				}
				edit = null;
				if (r.path !== cur.path) {
					onmoved(cur.path, r.path);
					model.follow(r.path);
				}
				scroller?.focus();
			} else {
				const r = await ops.create(cur.parent, cur.value, cur.mode);
				if (edit !== cur) return;
				if (!r.ok) {
					cur.error = r.message;
					return;
				}
				edit = null;
				model.follow(r.path, cur.mode === 'dir');
				if (cur.mode === 'file') onopen(r.path);
				scroller?.focus();
			}
		} finally {
			busy = false;
		}
	}

	function nameKeydown(e: KeyboardEvent) {
		// The field's keys are its own; the tree's type-ahead and shortcuts never see them.
		e.stopPropagation();
		if (e.key === 'Enter') {
			e.preventDefault();
			void commit(false);
		} else if (e.key === 'Escape') {
			e.preventDefault();
			cancelEdit();
		}
	}

	/** Focuses the name field and selects the part of the name before its extension. */
	function focusName(el: HTMLInputElement) {
		el.focus();
		const dot = el.value.lastIndexOf('.');
		el.setSelectionRange(0, dot > 0 ? dot : el.value.length);
	}

	/** The row a new entry's field follows: the folder's own row, or the chain row holding it. */
	const anchorPath = $derived.by(() => {
		if (edit === null || edit.mode === 'rename' || edit.parent === '') return null;
		const p = edit.parent;
		return view.rows.find((r) => r.dir && (r.path === p || r.path.startsWith(`${p}/`)))?.path ?? null;
	});
	const anchorDepth = $derived(
		edit === null || edit.parent === '' ? 0 : (view.rows.find((r) => r.path === anchorPath)?.depth ?? 0),
	);

	/** Moves the keyboard focus to the tree (the palette's "Focus the file tree"). */
	export function focus() {
		scroller?.focus();
	}

	function closeMenu() {
		menu = null;
		scroller?.focus();
	}

	function statusText(w: TreeView['watch']): string {
		if (w.state === 'waiting') return 'Waiting for the connection; the tree may be out of date.';
		if (w.state === 'paused') return 'Paused while the page is hidden.';
		if (w.state === 'error' || w.state === 'ended') return `Not following changes${w.detail === '' ? '' : `: ${w.detail}`}.`;
		return '';
	}
</script>

{#snippet nameField(depth: number)}
	{#if edit}
		<div class="row editing" style:--depth={depth}>
			<span class="guides" aria-hidden="true"></span>
			<span class="chevron" aria-hidden="true"></span>
			<span class="icon" aria-hidden="true">
				{#if edit.mode === 'dir'}<Folder size={16} />{:else}<File size={16} />{/if}
			</span>
			<input
				class="name"
				type="text"
				aria-label={edit.mode === 'dir' ? 'New folder name' : 'New file name'}
				aria-invalid={edit.error !== ''}
				bind:value={edit.value}
				{@attach focusName}
				onkeydown={nameKeydown}
				onclick={(e) => e.stopPropagation()}
				onblur={() => void commit(true)}
			/>
		</div>
		{#if edit.error !== ''}<p class="name-error" role="alert" style:--depth={depth}>{edit.error}</p>{/if}
	{/if}
{/snippet}

<svelte:document onvisibilitychange={() => watch.setHidden(document.hidden)} />
<svelte:window onpointerdown={(e) => { if (menu && !(e.target as Element).closest('.menu')) menu = null; }} />

<section class="files" aria-label="Files">
	<header>
		<label class="filter">
			<Search size={14} aria-hidden="true" />
			<input
				type="text"
				placeholder="Filter files"
				aria-label="Filter files"
				value={view.filter}
				oninput={(e) => model.setFilter(e.currentTarget.value)}
				onkeydown={(e) => {
					if (e.key === 'Escape') model.setFilter('');
				}}
			/>
			{#if view.filter !== ''}
				<button type="button" class="bare" aria-label="Clear the filter" onclick={() => model.setFilter('')}>
					<X size={14} />
				</button>
			{/if}
		</label>
		<button
			type="button"
			class="bare toggle"
			aria-pressed={view.showHidden}
			aria-label="Show hidden files"
			title={view.showHidden ? 'Hide hidden files' : 'Show hidden files'}
			onclick={() => model.setShowHidden(!view.showHidden)}
		>
			{#if view.showHidden}<Eye size={16} />{:else}<EyeOff size={16} />{/if}
		</button>
	</header>

	{#if view.error}
		<p class="note error">{view.error}</p>
	{/if}
	{#if statusText(view.watch) !== ''}
		<p class="note">{statusText(view.watch)}</p>
	{/if}

	<div
		class="viewport"
		role="tree"
		aria-label="Project files"
		tabindex="0"
		aria-activedescendant={selectedIndex >= win.start && selectedIndex < win.end ? `ft-${selectedIndex}` : undefined}
		bind:this={scroller}
		bind:clientHeight={viewportHeight}
		{onscroll}
		{onkeydown}
	>
		<div class="spacer" style:height="{win.totalHeight}px">
			<div class="rows" style:transform="translateY({win.offset}px)">
				{#if edit && edit.mode !== 'rename' && edit.parent === '' && win.start === 0}
					{@render nameField(1)}
				{/if}
				{#each shown as row, i (row.path)}
					<!-- svelte-ignore a11y_click_events_have_key_events -->
					<div
						class="row"
						class:selected={row.path === view.selected}
						id="ft-{win.start + i}"
						role="treeitem"
						tabindex="-1"
						aria-level={row.depth}
						aria-selected={row.path === view.selected}
						aria-expanded={row.dir ? row.expanded : undefined}
						style:--depth={row.depth}
						title={row.path}
						onclick={() => choose(row)}
						oncontextmenu={(e) => oncontextmenu(e, row)}
					>
						<span class="guides" aria-hidden="true"></span>
						<span class="chevron" class:open={row.expanded} aria-hidden="true">
							{#if row.dir}<ChevronRight size={12} />{/if}
						</span>
						{#if true}
							{@const Icon = ICONS[row.icon]}
							<span class="icon {row.icon}" aria-hidden="true"><Icon size={16} /></span>
						{/if}
						{#if edit && edit.mode === 'rename' && edit.path === row.path}
							<input
								class="name"
								type="text"
								aria-label="New name"
								aria-invalid={edit.error !== ''}
								bind:value={edit.value}
								{@attach focusName}
								onkeydown={nameKeydown}
								onclick={(e) => e.stopPropagation()}
								onblur={() => void commit(true)}
							/>
						{:else}
							<span class="label">{row.label}</span>
						{/if}
					</div>
					{#if edit && edit.mode === 'rename' && edit.path === row.path && edit.error !== ''}
						<p class="name-error" role="alert" style:--depth={row.depth}>{edit.error}</p>
					{/if}
					{#if edit && edit.mode !== 'rename' && anchorPath === row.path}
						{@render nameField(anchorDepth + 1)}
					{/if}
				{/each}
			</div>
		</div>
		{#if view.rows.length === 0}
			<p class="empty">{view.filter !== '' ? 'No file matches.' : view.watch.state === 'watching' ? 'This folder is empty.' : 'Loading...'}</p>
		{/if}
	</div>
	{#if view.more}
		<p class="note">Some folders hold more entries than can be listed; use the filter.</p>
	{/if}
</section>

{#if menu}
	{@const target = menu}
	<div class="menu" role="menu" tabindex="-1" style:left="{target.x}px" style:top="{target.y}px" onkeydown={(e) => { if (e.key === 'Escape') closeMenu(); }}>
		<button type="button" role="menuitem" onclick={() => void beginCreate('file', target.path)}>New file</button>
		<button type="button" role="menuitem" onclick={() => void beginCreate('dir', target.path)}>New folder</button>
		<button type="button" role="menuitem" onclick={() => beginRename(target.path)}>Rename</button>
		<button type="button" role="menuitem" onclick={() => void copy(model.absolutePath(target.path))}>Copy path</button>
		<button type="button" role="menuitem" onclick={() => void copy(model.relativePath(target.path))}>Copy relative path</button>
		{#if copied !== ''}<p class="note error">Not copied: {copied}</p>{/if}
	</div>
{/if}

<style>
	.files {
		display: flex;
		flex-direction: column;
		height: 100%;
		min-height: 0;
	}
	header {
		display: flex;
		align-items: center;
		gap: var(--sp-2);
		padding: var(--sp-2) var(--sp-3);
		border-bottom: 1px solid var(--border);
	}
	.filter {
		flex: 1;
		min-width: 0;
		display: flex;
		align-items: center;
		gap: var(--sp-2);
		padding: 0 var(--sp-3);
		border: 1px solid var(--border);
		border-radius: var(--radius-control);
		background: var(--bg-window);
		color: var(--text-3);
	}
	.filter:focus-within {
		border-color: var(--accent);
	}
	.filter input {
		flex: 1;
		min-width: 0;
		border: 0;
		background: transparent;
		padding: 0;
		outline: none;
		color: var(--text);
	}
	.bare {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		min-height: 0;
		padding: var(--sp-1);
		border: 0;
		background: transparent;
		color: var(--text-2);
		border-radius: var(--radius-control);
	}
	.bare:hover:not(:disabled) {
		background: var(--bg-hover);
		color: var(--text);
	}
	.toggle[aria-pressed='true'] {
		color: var(--accent);
		background: var(--accent-soft);
	}
	.note {
		margin: 0;
		padding: var(--sp-2) var(--sp-4);
		font-size: var(--fs-small);
		color: var(--text-2);
	}
	.note.error {
		color: var(--danger);
	}
	.viewport {
		flex: 1;
		min-height: 0;
		overflow-y: auto;
		overflow-x: hidden;
		outline: none;
		padding: var(--sp-2) 0;
	}
	.viewport:focus-visible {
		outline: 2px solid var(--accent);
		outline-offset: -2px;
	}
	.spacer {
		position: relative;
		overflow: hidden;
	}
	.row {
		--indent: 12px;
		position: relative;
		display: flex;
		align-items: center;
		gap: var(--sp-2);
		height: var(--row);
		margin: 0 var(--sp-2);
		padding-right: var(--sp-3);
		padding-left: calc(var(--sp-3) + (var(--depth) - 1) * var(--indent));
		border-radius: var(--radius-control);
		cursor: default;
		user-select: none;
		white-space: nowrap;
	}
	.row:hover {
		background: var(--bg-hover);
	}
	.row.selected {
		background: var(--accent-soft);
	}
	.guides {
		position: absolute;
		top: 0;
		bottom: 0;
		left: calc(var(--sp-3) + 6px);
		width: calc((var(--depth) - 1) * var(--indent));
		background-image: repeating-linear-gradient(to right, var(--border) 0 1px, transparent 1px var(--indent));
		pointer-events: none;
	}
	.chevron {
		flex: none;
		width: 12px;
		display: inline-flex;
		color: var(--text-3);
		transition: transform 0.1s;
	}
	.chevron.open {
		transform: rotate(90deg);
	}
	.icon {
		flex: none;
		display: inline-flex;
		color: var(--text-2);
	}
	.icon.folder,
	.icon.folder-open {
		color: var(--accent);
	}
	.icon.module {
		color: var(--success);
	}
	.icon.code {
		color: var(--text);
	}
	.icon.json {
		color: var(--warning);
	}
	.icon.image {
		color: var(--success);
	}
	.label {
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.name {
		flex: 1;
		min-width: 0;
		height: calc(var(--row) - 4px);
		padding: 0 var(--sp-2);
		border: 1px solid var(--accent);
		border-radius: var(--radius-control);
		background: var(--bg-window);
		color: var(--text);
		outline: none;
	}
	.name[aria-invalid='true'] {
		border-color: var(--danger);
	}
	.name-error {
		margin: 0 var(--sp-2);
		padding: 0 var(--sp-3) 0 calc(var(--sp-3) + 28px + (var(--depth) - 1) * 12px);
		font-size: var(--fs-small);
		color: var(--danger);
		white-space: normal;
	}
	.empty {
		margin: var(--sp-4);
		color: var(--text-3);
	}
	.menu {
		position: fixed;
		z-index: 10;
		min-width: 160px;
		padding: var(--sp-2);
		border: 1px solid var(--border);
		border-radius: var(--radius-panel);
		background: var(--overlay-solid);
		box-shadow: var(--shadow);
		display: flex;
		flex-direction: column;
	}
	.menu button {
		text-align: left;
		border: 0;
		background: transparent;
	}
	.menu button:hover,
	.menu button:focus-visible {
		background: var(--accent);
		color: var(--accent-text);
	}
</style>
