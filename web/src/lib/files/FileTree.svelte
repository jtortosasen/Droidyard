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
	import { Clipboard } from './clipboard';
	import CollisionDialog from './CollisionDialog.svelte';
	import { dragStarts, dropMode, dropTarget, edgeScroll, hoverStep, type Hover } from './drag';
	import { nameOf, parentOf, type FileOps, type Collision, type Notice, refuseName } from './ops';
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
		/** The project's id: the clipboard pastes only within it (D52). */
		workspaceId: string;
	}
	let { model, watch, onopen, ops, workspaceId }: Props = $props();

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
	// Clipboard, collisions and the notice (plan m2g step 7; D51-D56).
	const clip = new Clipboard();
	let cut = $state.raw<ReadonlySet<string>>(new Set());
	let notice = $state.raw<Notice | null>(untrack(() => ops.notice));
	let pending = $state.raw<Collision | null>(untrack(() => ops.pending));
	// The drag (D53): a candidate from the pointer down, then the ghost and the drop target.
	let drag = $state.raw<{ x: number; y: number; label: string; mode: 'move' | 'copy' } | null>(null);
	let dropDir = $state<string | null>(null);
	let dropRoot = $state(false);
	let revealSeen = 0;

	const win = $derived(
		rowWindow({ scrollTop, viewportHeight, rowHeight, overscan: OVERSCAN, count: view.rows.length, following: false }),
	);
	const shown = $derived(view.rows.slice(win.start, win.end));
	const selectedIndex = $derived(view.selected === null ? -1 : view.rows.findIndex((r) => r.path === view.selected));

	$effect(() => {
		const o = ops;
		const sync = () => {
			notice = o.notice;
			pending = o.pending;
		};
		sync();
		return o.subscribe(sync);
	});

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

	function choose(row: Row, e: MouseEvent) {
		if (justDragged) return;
		if (e.metaKey) {
			model.toggleSelect(row.path);
			return;
		}
		if (e.shiftKey) {
			model.selectRange(row.path);
			return;
		}
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
		if (e.shiftKey && !e.metaKey && !e.ctrlKey && !e.altKey && (e.key === 'ArrowUp' || e.key === 'ArrowDown')) {
			e.preventDefault();
			model.extendSelection(e.key === 'ArrowUp' ? -1 : 1);
			return;
		}
		if (e.metaKey && !e.ctrlKey && !e.altKey && !e.shiftKey) {
			const k = e.key.toLowerCase();
			const run: Record<string, () => void> = {
				x: () => cutSelection(),
				c: () => copySelection(),
				v: () => void pasteInto(selectedFolder()),
				backspace: () => void deleteSelection(),
				z: () => void ops.undo(),
			};
			if (run[k] !== undefined) {
				e.preventDefault();
				run[k]();
				return;
			}
		}
		if (e.metaKey || e.ctrlKey || e.altKey) return;
		const r = model.key(e.key);
		if (!r.handled) return;
		e.preventDefault();
		if (r.open !== undefined) onopen(r.open);
	}

	function syncCut() {
		cut = new Set(clip.mode === 'cut' ? clip.paths : []);
	}

	function cutSelection() {
		clip.put('cut', workspaceId, model.operands());
		syncCut();
	}

	function copySelection() {
		clip.put('copy', workspaceId, model.operands());
		syncCut();
	}

	/** Where a paste goes for the selected row: a folder, or a file's parent. */
	function selectedFolder(path: string | null = view.selected): string {
		const row = path === null ? undefined : view.rows.find((r) => r.path === path);
		return Clipboard.targetFor(path, row?.dir ?? false);
	}

	async function pasteInto(dir: string) {
		const plan = clip.paste(workspaceId, dir);
		syncCut();
		if (plan === null) return;
		await ops.paste(plan);
	}

	async function deleteSelection() {
		await ops.delete(model.operands());
	}

	function openMenu(path: string, x: number, y: number) {
		copied = '';
		if (!view.selection.has(path)) model.select(path);
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
				if (r.path !== cur.path) model.follow(r.path);
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

	// --- drag (D53): pointer events on the window for the drag's life --------------------
	let justDragged = false;
	let session: {
		down: { x: number; y: number };
		path: string;
		at: { x: number; y: number };
		alt: boolean;
		sources: string[] | null;
		hover: Hover | null;
		timer: ReturnType<typeof setInterval> | null;
	} | null = null;

	function rowAt(x: number, y: number): { row: Row | null; inside: boolean } {
		const el = document.elementFromPoint(x, y);
		if (!el || !scroller?.contains(el)) return { row: null, inside: false };
		const path = el.closest<HTMLElement>('[data-path]')?.dataset.path;
		return { row: path === undefined ? null : (view.rows.find((r) => r.path === path) ?? null), inside: true };
	}

	function target(): string | null {
		if (!session?.sources) return null;
		const { row, inside } = rowAt(session.at.x, session.at.y);
		return inside ? dropTarget(row, session.sources) : null;
	}

	/** One step of an active drag: the highlight, the expand timer, the edge scroll, the ghost. */
	function step() {
		const s = session;
		if (!s?.sources) return;
		const { row } = rowAt(s.at.x, s.at.y);
		const t = target();
		dropDir = t !== null && t !== '' ? t : null;
		dropRoot = t === '';
		const h = hoverStep(s.hover, row?.dir && !row.expanded ? row.path : null, Date.now());
		s.hover = h.state;
		if (h.expand && row) void model.expand(row.path);
		if (scroller) {
			const box = scroller.getBoundingClientRect();
			const d = edgeScroll(s.at.y, box.top, box.bottom);
			if (d !== 0) scroller.scrollTop += d;
		}
		const n = s.sources.length;
		drag = { x: s.at.x, y: s.at.y, label: n === 1 ? nameOf(s.sources[0]!) : `${n} items`, mode: dropMode(s.alt) };
	}

	function onpointerdown(e: PointerEvent, row: Row) {
		if (e.button !== 0 || e.pointerType === 'touch' || edit !== null || session !== null) return;
		session = { down: { x: e.clientX, y: e.clientY }, path: row.path, at: { x: e.clientX, y: e.clientY }, alt: e.altKey, sources: null, hover: null, timer: null };
		window.addEventListener('pointermove', pointermove);
		window.addEventListener('pointerup', pointerup);
		window.addEventListener('pointercancel', cancelDrag);
		window.addEventListener('keydown', dragKey, true);
		window.addEventListener('blur', cancelDrag);
	}

	function pointermove(e: PointerEvent) {
		const s = session;
		if (!s) return;
		s.at = { x: e.clientX, y: e.clientY };
		s.alt = e.altKey;
		if (!s.sources) {
			if (!dragStarts(s.down, s.at, e.buttons)) return;
			// A row of the selection drags the whole selection; any other row, itself.
			s.sources = view.selection.has(s.path) ? model.operands() : [s.path];
			if (!view.selection.has(s.path)) model.select(s.path);
			s.timer = setInterval(step, 100);
		}
		step();
	}

	function pointerup(e: PointerEvent) {
		const s = session;
		if (!s) return;
		s.alt = e.altKey;
		s.at = { x: e.clientX, y: e.clientY };
		const sources = s.sources;
		const t = target();
		const mode = dropMode(s.alt);
		endDrag();
		if (!sources) return;
		// The click that follows a drag is not a click.
		justDragged = true;
		setTimeout(() => (justDragged = false), 0);
		if (t === null) return;
		const items = sources
			.filter((p) => parentOf(p) !== t)
			.map((p) => ({ from: p, to: t === '' ? nameOf(p) : `${t}/${nameOf(p)}` }));
		if (items.length === 0) return;
		void (mode === 'copy' ? ops.copy(items) : ops.move(items));
	}

	function dragKey(e: KeyboardEvent) {
		if (session?.sources && e.key === 'Escape') {
			e.preventDefault();
			e.stopPropagation();
			cancelDrag();
		} else if (session) {
			session.alt = e.altKey;
			step();
		}
	}

	function cancelDrag() {
		const dragged = session?.sources != null;
		endDrag();
		if (dragged) {
			justDragged = true;
			setTimeout(() => (justDragged = false), 0);
		}
	}

	function endDrag() {
		window.removeEventListener('pointermove', pointermove);
		window.removeEventListener('pointerup', pointerup);
		window.removeEventListener('pointercancel', cancelDrag);
		window.removeEventListener('keydown', dragKey, true);
		window.removeEventListener('blur', cancelDrag);
		if (session?.timer) clearInterval(session.timer);
		session = null;
		drag = null;
		dropDir = null;
		dropRoot = false;
	}

	$effect(() => endDrag);

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
		aria-multiselectable="true"
		class:drop-root={dropRoot}
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
						class:selected={view.selection.has(row.path)}
						class:cut={cut.has(row.path)}
						class:drop={dropDir === row.path}
						data-path={row.path}
						data-cut={cut.has(row.path) ? 'true' : undefined}
						data-drop={dropDir === row.path ? 'true' : undefined}
						id="ft-{win.start + i}"
						role="treeitem"
						tabindex="-1"
						aria-level={row.depth}
						aria-selected={view.selection.has(row.path)}
						aria-expanded={row.dir ? row.expanded : undefined}
						style:--depth={row.depth}
						title={row.path}
						onclick={(e) => choose(row, e)}
						onpointerdown={(e) => onpointerdown(e, row)}
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
							<span class="label" class:folder={row.dir}>{row.label}</span>
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
	{#if notice}
		<p class="note notice" role="status" data-testid="file-notice">
			<span class="notice-text">{notice.text}</span>
			{#if notice.undoable}
				<button type="button" class="bare undo" onclick={() => void ops.undo()}>Undo</button>
			{/if}
		</p>
	{/if}
</section>

{#if pending}
	<CollisionDialog {pending} onkeep={() => void ops.resolveCollision('keepBoth')} onskip={() => void ops.resolveCollision('skip')} />
{/if}

{#if drag}
	<div class="drag-ghost" data-mode={drag.mode} aria-hidden="true" style:left="{drag.x + 12}px" style:top="{drag.y + 12}px">
		{drag.mode === 'copy' ? '+ ' : ''}{drag.label}
	</div>
{/if}

{#if menu}
	{@const target = menu}
	<div class="menu" role="menu" tabindex="-1" style:left="{target.x}px" style:top="{target.y}px" onkeydown={(e) => { if (e.key === 'Escape') closeMenu(); }}>
		<button type="button" role="menuitem" onclick={() => void beginCreate('file', target.path)}>New file</button>
		<button type="button" role="menuitem" onclick={() => void beginCreate('dir', target.path)}>New folder</button>
		<button type="button" role="menuitem" onclick={() => beginRename(target.path)}>Rename</button>
		<button type="button" role="menuitem" onclick={() => { cutSelection(); closeMenu(); }}>Cut</button>
		<button type="button" role="menuitem" onclick={() => { copySelection(); closeMenu(); }}>Copy</button>
		<button type="button" role="menuitem" disabled={!clip.canPaste(workspaceId)} onclick={() => { const dir = selectedFolder(target.path); closeMenu(); void pasteInto(dir); }}>Paste</button>
		<button type="button" role="menuitem" onclick={() => { const p = model.operands(); closeMenu(); void ops.duplicate(p); }}>Duplicate</button>
		<button type="button" role="menuitem" onclick={() => { closeMenu(); void deleteSelection(); }}>Delete</button>
		<button type="button" role="menuitem" disabled={!ops.canUndo} onclick={() => { closeMenu(); void ops.undo(); }}>Undo</button>
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
		min-height: var(--head);
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
		font-size: var(--fs-base);
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
	.row:hover:not(.selected) {
		background: var(--bg-hover);
	}
	/* Cut rows are muted by colour, not by opacity. */
	.row.cut,
	.row.cut .icon,
	.row.cut .chevron {
		color: var(--text-3);
	}
	.row.drop {
		background: var(--accent-soft);
		outline: 1px solid var(--accent);
		outline-offset: -1px;
	}
	.viewport.drop-root {
		outline: 1px solid var(--accent);
		outline-offset: -1px;
	}
	.notice {
		display: flex;
		align-items: center;
		gap: var(--sp-2);
		border-top: 1px solid var(--border);
	}
	.notice-text {
		flex: 1;
		min-width: 0;
		overflow-wrap: anywhere;
	}
	.undo {
		color: var(--accent);
	}
	.drag-ghost {
		position: fixed;
		z-index: 20;
		pointer-events: none;
		max-width: 260px;
		padding: var(--sp-1) var(--sp-3);
		border: 1px solid var(--border);
		border-radius: var(--radius-control);
		background: var(--overlay-solid);
		color: var(--text);
		box-shadow: var(--shadow);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
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
		font-family: var(--font-mono);
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
		color: var(--text-2);
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
	.label.folder {
		color: var(--text-2);
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
		font-size: var(--fs-base);
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
	.menu button:disabled {
		opacity: 0.5;
	}
	.menu button:hover:not(:disabled),
	.menu button:focus-visible {
		background: var(--accent);
		color: var(--accent-text);
	}
</style>
