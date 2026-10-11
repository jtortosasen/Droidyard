<script lang="ts">
	import type { Component, Snippet } from 'svelte';
	import Bug from '@lucide/svelte/icons/bug';
	import FileCode from '@lucide/svelte/icons/file-code';
	import FileDiff from '@lucide/svelte/icons/file-diff';
	import FolderGit from '@lucide/svelte/icons/folder-git';
	import FolderTree from '@lucide/svelte/icons/folder-tree';
	import GitBranch from '@lucide/svelte/icons/git-branch';
	import MonitorSmartphone from '@lucide/svelte/icons/monitor-smartphone';
	import PanelBottomClose from '@lucide/svelte/icons/panel-bottom-close';
	import Pin from '@lucide/svelte/icons/pin';
	import PanelLeftClose from '@lucide/svelte/icons/panel-left-close';
	import PanelRightClose from '@lucide/svelte/icons/panel-right-close';
	import Smartphone from '@lucide/svelte/icons/smartphone';
	import Rows3 from '@lucide/svelte/icons/rows-3';
	import ScrollText from '@lucide/svelte/icons/scroll-text';
	import Terminal from '@lucide/svelte/icons/terminal';
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import Activity from '@lucide/svelte/icons/activity';
	import Hammer from '@lucide/svelte/icons/hammer';
	import { browserStorage, clampSessionsSize, clampSize, closeSessions, loadLayout, openSessions, saveLayout, setRegion, type Region } from './layout';
	import { activate, disabledTitle, initialActive, panelsIn, type PanelDef, type PanelRegion, type ShellApi } from './panels';
	import { PeekModel } from './peek';
	import Splitter from './Splitter.svelte';

	// The window the owner described (plan m2d step 2, m2e step 5): a toolbar, the sessions
	// column, the left panel, the centre with its first-level tabs, the right panel and the
	// bottom tabs, and a status bar, over the registry in panels.ts. Left, right and bottom
	// each collapse to a 24 px strip (D32). The shell knows regions, not panels: `content` renders the
	// panel with the id it is given. Every panel stays mounted when its tab is not the
	// current one or its region is collapsed, so a stream (Logcat, the console) keeps
	// running and a filter keeps its text.
	interface Props {
		/** The panel with the given id; the second argument shows another panel's tab. */
		content: Snippet<[string, ShellApi]>;
		/** The status bar's content, the shell's last row (D35). */
		statusbar: Snippet;
		/** The toolbar's right-aligned group opens with it (the build status). */
		centre?: Snippet;
		/** Full-width strips under the toolbar (the trust banner). */
		banners?: Snippet;
		/** The toolbar's first control: the sessions toggle (D33), given the column's state. */
		sessionsToggle: Snippet<[{ open: boolean; toggle: () => void; attract: number; register: (el: HTMLElement | null) => void }]>;
		/** The sessions column's body, under its title and the density switch. */
		sessions: Snippet;
		/** Page-level overlays (the command palette), given what only the shell knows. */
		overlay?: Snippet<[ShellApi]>;
	}
	let { content, statusbar, centre, banners, sessionsToggle, sessions, overlay }: Props = $props();

	const storage = browserStorage(window);
	let layout = $state(loadLayout(storage));
	let active = $state(initialActive());

	$effect(() => {
		saveLayout(storage, $state.snapshot(layout));
	});

	const ICONS: Record<string, Component> = {
		files: FolderTree,
		'git-side': FolderGit,
		terminal: Terminal,
		editor: FileCode,
		diff: FileDiff,
		logcat: Rows3,
		applogs: Bug,
		console: ScrollText,
		problems: TriangleAlert,
		activity: Activity,
		git: GitBranch,
		liveview: MonitorSmartphone,
	};

	function show(id: string) {
		active = activate(active, id);
	}
	// The layout transitions are the pure functions of layout.ts (D33); they take a plain
	// snapshot and the result replaces the state.
	function set(next: ReturnType<typeof loadLayout>) {
		layout = next;
	}
	const api: ShellApi = {
		density: () => layout.density,
		setDensity: (d) => (layout.density = d),
		openSessions: () => set(openSessions($state.snapshot(layout))),
		showFiles: () => {
			set(setRegion($state.snapshot(layout), 'left', true));
			active = activate(active, 'files');
		},
		show,
	};
	function region(r: Region, open: boolean) {
		set(setRegion($state.snapshot(layout), r, open));
	}
	function toggleSessions() {
		const l = $state.snapshot(layout);
		set(l.sessions.open ? closeSessions(l) : openSessions(l));
	}
	/** A bottom strip button: restores the region and selects the tab. */
	function restoreBottom(id: string) {
		region('bottom', true);
		show(id);
	}
	function resize(region: Region, n: number) {
		layout.size[region] = clampSize(region, n);
	}

	// The sessions overlay (ADR-0052, plan m2f D43-D45). PeekModel decides from geometry; the
	// shell feeds it one coalesced pointer sample per frame and draws its view. The overlay
	// never touches `layout`: nothing it does is saved.
	const peek = new PeekModel();
	let view = $state.raw(peek.view);
	let toolbarEl: HTMLElement | undefined;
	let middleEl: HTMLElement | undefined;
	let overlayEl: HTMLElement | null = $state(null);
	let toggleEl: HTMLElement | null = null;
	function registerToggle(el: HTMLElement | null) {
		toggleEl = el;
	}

	$effect(() => {
		peek.setDocked(layout.sessions.open);
		peek.setWidth(layout.sessions.size);
	});

	$effect(() => {
		const off = peek.subscribe(() => (view = peek.view));
		const doc = document.documentElement;
		let last: PointerEvent | null = null;
		let frame = 0;

		const sample = () => {
			frame = 0;
			const e = last;
			if (!e || !toolbarEl || !middleEl) return;
			const top = toolbarEl.getBoundingClientRect().top;
			const bottom = middleEl.getBoundingClientRect().bottom;
			const inBand = e.clientY >= top && e.clientY <= bottom && document.querySelector('dialog:modal') === null;
			let onToggle = false;
			if (toggleEl) {
				const r = toggleEl.getBoundingClientRect();
				onToggle = e.clientX >= r.left && e.clientX <= r.right && e.clientY >= r.top && e.clientY <= r.bottom;
			}
			peek.pointer({ x: e.clientX, inBand, onToggle, buttons: e.buttons, pointerType: e.pointerType });
		};
		const onMove = (e: PointerEvent) => {
			last = e;
			if (frame === 0) frame = requestAnimationFrame(sample);
		};
		const onLeave = (e: PointerEvent) => {
			// D43: only a mouse drives the overlay; a touch ends with a pointerleave after every tap.
			if (e.pointerType !== 'mouse') return;
			last = null;
			if (frame !== 0) cancelAnimationFrame(frame);
			frame = 0;
			peek.windowLeave();
		};
		const onDown = (e: PointerEvent) => {
			const path = e.composedPath();
			if ((overlayEl && path.includes(overlayEl)) || (toggleEl && path.includes(toggleEl))) return;
			peek.pointerDownOutside();
		};
		const onKey = (e: KeyboardEvent) => {
			if (e.key === 'Escape') peek.escape();
		};
		const mq = window.matchMedia('(prefers-reduced-motion: reduce)');
		const onMq = () => peek.setReducedMotion(mq.matches);
		onMq();

		window.addEventListener('pointermove', onMove, { passive: true });
		doc.addEventListener('pointerleave', onLeave, { passive: true });
		window.addEventListener('pointerdown', onDown, { capture: true, passive: true });
		window.addEventListener('keydown', onKey);
		mq.addEventListener('change', onMq);
		return () => {
			window.removeEventListener('pointermove', onMove);
			doc.removeEventListener('pointerleave', onLeave);
			window.removeEventListener('pointerdown', onDown, { capture: true });
			window.removeEventListener('keydown', onKey);
			mq.removeEventListener('change', onMq);
			if (frame !== 0) cancelAnimationFrame(frame);
			off();
			peek.dispose();
		};
	});

	// D43: focus in a text field keeps the overlay (its typed text survives); a clicked button
	// takes focus too in Chrome, and it must not pin the overlay open.
	function textEntry(el: EventTarget | null): boolean {
		return el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement || (el instanceof HTMLElement && el.isContentEditable);
	}
	function focusChange(e: FocusEvent) {
		const now = e.type === 'focusin' ? e.target : e.relatedTarget;
		peek.setFocusInside(textEntry(now) && now instanceof Node && overlayEl !== null && overlayEl.contains(now));
	}
</script>

{#snippet tabs(region: PanelRegion, label: string)}
	<div class="tabs" role="tablist" aria-label={label}>
		{#each panelsIn(region) as p (p.id)}
			{@render tab(p, region)}
		{/each}
	</div>
{/snippet}

{#snippet tab(p: PanelDef, region: PanelRegion)}
	{@const Icon = ICONS[p.id]}
	<button
		type="button"
		role="tab"
		class="tab"
		class:current={active[region] === p.id}
		aria-selected={active[region] === p.id}
		disabled={p.disabled !== undefined}
		title={disabledTitle(p)}
		onclick={() => (active = activate(active, p.id))}
	>
		{#if Icon}<Icon size={14} />{/if}
		<span>{p.label}</span>
		{#if p.disabled}<span class="soon">{p.disabled.milestone}</span>{/if}
	</button>
{/snippet}

{#snippet column(overlay: boolean)}
	<div class="column-head">
		<div class="title-row">
			<h2>Sessions</h2>
			{#if overlay}
				<button type="button" class="icon-button" title="Dock the sessions column" aria-label="Dock the sessions column" onclick={toggleSessions}><Pin size={14} /></button>
			{/if}
		</div>
		<div class="segmented" role="group" aria-label="Density">
			<button type="button" class="pill" class:current={layout.density === 'compact'} aria-pressed={layout.density === 'compact'} onclick={() => (layout.density = 'compact')}>Compact</button>
			<button type="button" class="pill" class:current={layout.density === 'comfortable'} aria-pressed={layout.density === 'comfortable'} onclick={() => (layout.density = 'comfortable')}>Comfortable</button>
		</div>
	</div>
	<div class="body scroll">{@render sessions()}</div>
{/snippet}

{#snippet panes(region: PanelRegion)}
	{#each panelsIn(region).filter((p) => p.disabled === undefined) as p (p.id)}
		<div class="pane" class:current={active[region] === p.id} role="tabpanel" aria-label={p.label}>
			{@render content(p.id, api)}
		</div>
	{/each}
{/snippet}

<div class="shell" data-density={layout.density}>
	<header class="toolbar" bind:this={toolbarEl}>
		{@render sessionsToggle({ open: layout.sessions.open, toggle: toggleSessions, attract: view.attract, register: registerToggle })}
		{#each panelsIn('toolbar').filter((p) => !p.end) as p (p.id)}
			<div class="tool">{@render content(p.id, api)}</div>
		{/each}
		<span class="spacer"></span>
		{#if centre}{@render centre()}{/if}
		{#each panelsIn('toolbar').filter((p) => p.end) as p (p.id)}
			<div class="tool">{@render content(p.id, api)}</div>
		{/each}
	</header>
	{#if banners}{@render banners()}{/if}

	<div class="middle" bind:this={middleEl}>
		{#if layout.sessions.open}
			<aside class="sessions" style:width="{layout.sessions.size}px" aria-label="Sessions">
				{@render column(false)}
			</aside>
			<Splitter axis="x" sign={1} value={layout.sessions.size} clamp={clampSessionsSize} onchange={(n) => (layout.sessions.size = clampSessionsSize(n))} label="Resize the sessions column" />
		{:else if view.state !== 'hidden'}
			<aside
				class="sessions overlay"
				class:shown={view.state === 'shown'}
				bind:this={overlayEl}
				style:width="{layout.sessions.size}px"
				style:transform={view.state === 'shown' ? 'translateX(0)' : `translateX(calc(-100% + ${view.offset}px))`}
				aria-label="Sessions"
				aria-hidden={view.state === 'feint' ? 'true' : undefined}
				inert={view.state === 'feint'}
				onfocusin={focusChange}
				onfocusout={focusChange}
			>
				{@render column(true)}
			</aside>
		{/if}

		{#if !layout.open.left}
			<div class="strip vertical">
				<button type="button" class="icon-button" title="Restore the left panel" aria-label="Restore the left panel" onclick={() => region('left', true)}><FolderTree size={14} /></button>
			</div>
		{/if}
		<aside class="left" class:collapsed={!layout.open.left} style:width="{layout.size.left}px" aria-label="Left panel">
			<div class="head">
				<div class="segmented" role="tablist" aria-label="Left panel">
					{#each panelsIn('left') as p (p.id)}
						<button
							type="button"
							role="tab"
							class="pill"
							class:current={active.left === p.id}
							aria-selected={active.left === p.id}
							disabled={p.disabled !== undefined}
							title={disabledTitle(p)}
							onclick={() => (active = activate(active, p.id))}
						>
							{p.label}
						</button>
					{/each}
				</div>
				<button type="button" class="icon-button" title="Minimize the left panel" aria-label="Minimize the left panel" onclick={() => region('left', false)}><PanelLeftClose size={14} /></button>
			</div>
			<div class="body">{@render panes('left')}</div>
		</aside>
		{#if layout.open.left}
			<Splitter axis="x" sign={1} value={layout.size.left} clamp={(n) => clampSize('left', n)} onchange={(n) => resize('left', n)} label="Resize the left panel" />
		{/if}

		<main class="centre">
			{@render tabs('centre', 'Editor area')}
			<div class="body">{@render panes('centre')}</div>
		</main>

		{#if layout.open.right}
			<Splitter axis="x" sign={-1} value={layout.size.right} clamp={(n) => clampSize('right', n)} onchange={(n) => resize('right', n)} label="Resize the right panel" />
		{/if}
		<aside class="right" class:collapsed={!layout.open.right} style:width="{layout.size.right}px" aria-label="Device panel">
			<div class="head right-head">
				<span class="caption">Device</span>
				<button type="button" class="icon-button" title="Minimize the right panel" aria-label="Minimize the right panel" onclick={() => region('right', false)}><PanelRightClose size={14} /></button>
			</div>
			<div class="body scroll">
				{#each panelsIn('right') as p (p.id)}
					{#if p.disabled}
						{@const Icon = ICONS[p.id]}
						<section class="reserved" aria-label={p.label}>
							<h2>{p.label}</h2>
							<div class="frame">
								{#if Icon}<Icon size={28} />{/if}
								<p>{p.label} arrives with the {p.disabled.milestone} milestone.</p>
							</div>
						</section>
					{:else}
						<div class="rsec">{@render content(p.id, api)}</div>
					{/if}
				{/each}
			</div>
		</aside>
		{#if !layout.open.right}
			<div class="strip vertical">
				<button type="button" class="icon-button" title="Restore the right panel" aria-label="Restore the right panel" onclick={() => region('right', true)}><Smartphone size={14} /></button>
			</div>
		{/if}
	</div>

	{#if layout.open.bottom}
		<Splitter axis="y" sign={-1} value={layout.size.bottom} clamp={(n) => clampSize('bottom', n)} onchange={(n) => resize('bottom', n)} label="Resize the bottom panel" />
	{:else}
		<div class="strip horizontal" role="toolbar" aria-label="Bottom panel (minimized)">
			{#each panelsIn('bottom').filter((p) => p.disabled === undefined) as p (p.id)}
				{@const Icon = ICONS[p.id]}
				<button type="button" class="tab" title="Show {p.label}" onclick={() => restoreBottom(p.id)}>
					{#if Icon}<Icon size={14} />{/if}
					<span>{p.label}</span>
				</button>
			{/each}
		</div>
	{/if}
	<section class="bottom" class:collapsed={!layout.open.bottom} style:height="{layout.size.bottom}px" aria-label="Bottom panel">
		<div class="head">
			{@render tabs('bottom', 'Bottom panel')}
			<button type="button" class="icon-button" title="Minimize the bottom panel" aria-label="Minimize the bottom panel" onclick={() => region('bottom', false)}><PanelBottomClose size={14} /></button>
		</div>
		<div class="body">{@render panes('bottom')}</div>
	</section>

	<footer class="statusbar" aria-label="Status">
		{@render statusbar()}
	</footer>
	{#if overlay}{@render overlay(api)}{/if}
</div>

<style>
	.shell {
		display: flex;
		flex-direction: column;
		height: 100vh;
		overflow: hidden;
		background: var(--bg-window);
	}
	.collapsed {
		display: none !important;
	}

	.toolbar {
		flex: none;
		display: flex;
		align-items: center;
		gap: var(--sp-3);
		padding: var(--sp-2) var(--sp-4);
		height: 38px;
		flex-wrap: nowrap;
		overflow: hidden;
		background: var(--bg-raised);
		border-bottom: 1px solid var(--border);
	}
	.tool {
		flex: none;
		display: flex;
		align-items: center;
		gap: var(--sp-3);
		min-width: 0;
	}
	.tool :global(section),
	.tool :global(div) {
		display: flex;
		align-items: center;
		flex-wrap: nowrap;
		gap: var(--sp-3);
	}
	.tool :global(h2),
	.tool :global(p) {
		margin: 0;
	}
	.tool :global(h2) {
		position: absolute;
		width: 1px;
		height: 1px;
		overflow: hidden;
		clip-path: inset(50%);
	}
	.spacer {
		flex: 1;
	}
	.middle {
		flex: 1;
		min-height: 0;
		display: flex;
		position: relative;
	}
	.sessions {
		flex: none;
		display: flex;
		flex-direction: column;
		min-width: 0;
		min-height: 0;
		background: var(--bg-panel);
		border-right: 1px solid var(--border);
	}
	/* The overlay floats over the tree and splitters without moving any box (D43); in the
	   feint it is inert and takes no pointer. */
	.sessions.overlay {
		position: absolute;
		top: 0;
		bottom: 0;
		left: 0;
		z-index: 20;
	}
	.sessions.overlay:not(.shown) {
		pointer-events: none;
	}
	/* Shown, from the feint or straight from a hover, it slides in (D44: 160 ms ease-out). */
	.sessions.overlay.shown {
		box-shadow: 0 0 16px rgb(0 0 0 / 0.35);
		animation: slide-in 160ms ease-out;
	}
	@keyframes slide-in {
		from {
			transform: translateX(-100%);
		}
		to {
			transform: translateX(0);
		}
	}
	@media (prefers-reduced-motion: reduce) {
		.sessions.overlay.shown {
			animation: none;
		}
	}
	.title-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		margin-bottom: var(--sp-3);
	}
	.title-row h2 {
		margin: 0;
	}
	.column-head {
		flex: none;
		padding: var(--sp-4);
		border-bottom: 1px solid var(--border);
	}
	.column-head h2 {
		margin: 0 0 var(--sp-3);
		font-size: var(--fs-small);
		color: var(--text-2);
		font-weight: 600;
	}
	.column-head .segmented {
		margin: 0;
	}
	.head {
		flex: none;
		display: flex;
		align-items: center;
		padding-right: var(--sp-3);
		background: var(--bg-raised);
		border-bottom: 1px solid var(--border);
	}
	.head .segmented,
	.head .tabs {
		flex: 1;
		min-width: 0;
		border-bottom: 0;
	}
	.head .segmented {
		margin: var(--sp-3);
	}
	.right-head {
		justify-content: space-between;
		padding-left: var(--sp-4);
		min-height: var(--tab-height);
	}
	.caption {
		font-size: var(--fs-small);
		color: var(--text-2);
	}
	.strip {
		flex: none;
		display: flex;
		background: var(--bg-raised);
	}
	.strip.vertical {
		flex-direction: column;
		align-items: center;
		width: 24px;
		padding-top: var(--sp-3);
		border-right: 1px solid var(--border);
		border-left: 1px solid var(--border);
	}
	.strip.horizontal {
		height: 24px;
		align-items: center;
		gap: var(--sp-1);
		padding: 0 var(--sp-3);
		overflow-x: auto;
		border-top: 1px solid var(--border);
	}
	.strip .tab {
		height: 22px;
		border-bottom: 0;
	}
	.statusbar {
		flex: none;
		display: flex;
		align-items: center;
		gap: var(--sp-5);
		height: 24px;
		padding: 0 var(--sp-5);
		overflow: hidden;
		background: var(--bg-raised);
		border-top: 1px solid var(--border);
		font-size: var(--fs-small);
		color: var(--text-2);
		white-space: nowrap;
	}

	.left,
	.right,
	.centre,
	.bottom {
		display: flex;
		flex-direction: column;
		min-width: 0;
		min-height: 0;
		background: var(--bg-panel);
	}
	.left,
	.right {
		flex: none;
	}
	.centre {
		flex: 1;
		min-width: 240px;
	}
	.bottom {
		flex: none;
		border-top: 1px solid var(--border);
	}
	.body {
		flex: 1;
		min-height: 0;
		min-width: 0;
		position: relative;
		display: flex;
		flex-direction: column;
	}
	.body.scroll {
		overflow: auto;
		display: block;
	}

	.tabs {
		flex: none;
		display: flex;
		gap: var(--sp-1);
		padding: 0 var(--sp-3);
		overflow-x: auto;
		background: var(--bg-raised);
		border-bottom: 1px solid var(--border);
	}
	.tab {
		display: inline-flex;
		align-items: center;
		gap: var(--sp-3);
		height: var(--tab-height);
		min-height: 0;
		padding: 0 var(--sp-4);
		border: 0;
		border-bottom: 2px solid transparent;
		border-radius: 0;
		background: transparent;
		color: var(--text-2);
		font-size: var(--fs-small);
		white-space: nowrap;
	}
	.tab:hover:not(:disabled) {
		background: var(--bg-hover);
		color: var(--text);
	}
	.tab.current {
		color: var(--text);
		border-bottom-color: var(--accent);
	}
	.tab:disabled {
		opacity: 1;
		color: var(--text-3);
	}
	.soon {
		font-size: 9px;
		padding: 0 var(--sp-2);
		border: 1px solid var(--border);
		border-radius: var(--radius-control);
	}

	.segmented {
		flex: none;
		display: flex;
		margin: var(--sp-4);
		padding: var(--sp-1);
		border: 1px solid var(--border);
		border-radius: var(--radius-control);
		background: var(--bg-window);
	}
	.pill {
		flex: 1;
		min-height: 0;
		height: calc(var(--row) - 4px);
		border: 0;
		border-radius: calc(var(--radius-control) - 2px);
		background: transparent;
		color: var(--text-2);
		font-size: var(--fs-small);
	}
	.pill.current {
		background: var(--bg-active);
		color: var(--text);
	}
	.pill:disabled {
		opacity: 1;
		color: var(--text-3);
	}

	.pane {
		display: none;
		flex: 1;
		min-height: 0;
		overflow: auto;
		padding: var(--sp-4);
	}
	.pane.current {
		display: block;
	}
	/* The bottom panels keep their controls: the pane is a column and one list in the panel
	   is the scroller; the pane itself scrolls only below the list's floor (plan m2f D46). */
	.bottom .pane.current {
		display: flex;
		flex-direction: column;
		overflow-y: auto;
	}
	/* A tab's name is already on the tab; the panel's own heading is for screen readers. */
	.pane :global(h2) {
		position: absolute;
		width: 1px;
		height: 1px;
		overflow: hidden;
		clip-path: inset(50%);
	}

	.rsec,
	.reserved {
		padding: var(--sp-4);
		border-bottom: 1px solid var(--border);
	}
	/* The live view's reserved area: the shape of a phone screen, empty (no stills). */
	.reserved {
		color: var(--text-3);
	}
	.reserved .frame {
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		gap: var(--sp-2);
		min-height: 64px;
		padding: var(--sp-3);
		border: 1px dashed var(--border);
		border-radius: var(--radius-panel);
		background: var(--bg-window);
		text-align: center;
		font-size: var(--fs-small);
	}
	.reserved .frame p {
		margin: 0;
	}
</style>
