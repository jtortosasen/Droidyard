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
	}
	let { model, watch, onopen }: Props = $props();

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
						<span class="label">{row.label}</span>
					</div>
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
