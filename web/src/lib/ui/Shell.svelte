<script lang="ts">
	import type { Component, Snippet } from 'svelte';
	import AppWindow from '@lucide/svelte/icons/app-window';
	import Bug from '@lucide/svelte/icons/bug';
	import FileCode from '@lucide/svelte/icons/file-code';
	import FileDiff from '@lucide/svelte/icons/file-diff';
	import FolderGit from '@lucide/svelte/icons/folder-git';
	import FolderTree from '@lucide/svelte/icons/folder-tree';
	import GitBranch from '@lucide/svelte/icons/git-branch';
	import MonitorSmartphone from '@lucide/svelte/icons/monitor-smartphone';
	import PanelBottom from '@lucide/svelte/icons/panel-bottom';
	import PanelLeft from '@lucide/svelte/icons/panel-left';
	import PanelRight from '@lucide/svelte/icons/panel-right';
	import Rows3 from '@lucide/svelte/icons/rows-3';
	import ScrollText from '@lucide/svelte/icons/scroll-text';
	import Terminal from '@lucide/svelte/icons/terminal';
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import Activity from '@lucide/svelte/icons/activity';
	import Hammer from '@lucide/svelte/icons/hammer';
	import { browserStorage, clampSize, loadLayout, saveLayout, type Region } from './layout';
	import { activate, disabledTitle, initialActive, panelsIn, type PanelDef, type PanelRegion, type ShellApi } from './panels';
	import Splitter from './Splitter.svelte';

	// The window the owner described (plan m2d step 2): a toolbar, a rail, the left panel,
	// the centre with its first-level tabs, the right panel and the bottom tabs, over the
	// registry in panels.ts. The shell knows regions, not panels: `content` renders the
	// panel with the id it is given. Every panel stays mounted when its tab is not the
	// current one or its region is collapsed, so a stream (Logcat, the console) keeps
	// running and a filter keeps its text.
	interface Props {
		/** The panel with the given id; the second argument shows another panel's tab. */
		content: Snippet<[string, (id: string) => void]>;
		status: Snippet;
		/** The toolbar's centre (the build status). */
		centre?: Snippet;
		/** Full-width strips under the toolbar (the trust banner). */
		banners?: Snippet;
		/** The projects icon and its overlay, the rail's first item (the overlay anchors to the middle row). */
		rail?: Snippet;
		/** Page-level overlays (the command palette), given what only the shell knows. */
		overlay?: Snippet<[ShellApi]>;
	}
	let { content, status, centre, banners, rail, overlay }: Props = $props();

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
	const api: ShellApi = {
		density: () => layout.density,
		setDensity: (d) => (layout.density = d),
		showFiles: () => {
			layout.open.left = true;
			active = activate(active, 'files');
		},
		show,
	};
	function toggle(region: Region) {
		layout.open[region] = !layout.open[region];
	}
	function resize(region: Region, n: number) {
		layout.size[region] = clampSize(region, n);
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

{#snippet panes(region: PanelRegion)}
	{#each panelsIn(region).filter((p) => p.disabled === undefined) as p (p.id)}
		<div class="pane" class:current={active[region] === p.id} role="tabpanel" aria-label={p.label}>
			{@render content(p.id, show)}
		</div>
	{/each}
{/snippet}

<div class="shell" data-density={layout.density}>
	<header class="toolbar">
		<strong class="brand"><AppWindow size={16} /> Droidyard</strong>
		{#each panelsIn('toolbar') as p (p.id)}
			<div class="tool">{@render content(p.id, show)}</div>
		{/each}
		<span class="spacer"></span>
		{#if centre}{@render centre()}{/if}
		<span class="spacer"></span>
		{@render status()}
		<button
			type="button"
			class="icon-button"
			title={layout.density === 'compact' ? 'Density: compact (switch to comfortable)' : 'Density: comfortable (switch to compact)'}
			onclick={() => (layout.density = layout.density === 'compact' ? 'comfortable' : 'compact')}
		>
			{layout.density === 'compact' ? 'Compact' : 'Comfortable'}
		</button>
		<span class="toggles">
			<button type="button" class="icon-button" title="Toggle the left panel" aria-pressed={layout.open.left} onclick={() => toggle('left')}><PanelLeft size={14} /></button>
			<button type="button" class="icon-button" title="Toggle the bottom panel" aria-pressed={layout.open.bottom} onclick={() => toggle('bottom')}><PanelBottom size={14} /></button>
			<button type="button" class="icon-button" title="Toggle the right panel" aria-pressed={layout.open.right} onclick={() => toggle('right')}><PanelRight size={14} /></button>
		</span>
	</header>
	{#if banners}{@render banners()}{/if}

	<div class="middle">
		<nav class="rail" aria-label="Sidebar">
			{#if rail}{@render rail()}{/if}
			<button type="button" class="rail-button" class:current={layout.open.left} aria-pressed={layout.open.left} title="Files" onclick={() => toggle('left')}><FolderTree size={16} /></button>
		</nav>

		<aside class="left" class:collapsed={!layout.open.left} style:width="{layout.size.left}px" aria-label="Left panel">
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
						<div class="rsec">{@render content(p.id, show)}</div>
					{/if}
				{/each}
			</div>
		</aside>
	</div>

	{#if layout.open.bottom}
		<Splitter axis="y" sign={-1} value={layout.size.bottom} clamp={(n) => clampSize('bottom', n)} onchange={(n) => resize('bottom', n)} label="Resize the bottom panel" />
	{/if}
	<section class="bottom" class:collapsed={!layout.open.bottom} style:height="{layout.size.bottom}px" aria-label="Bottom panel">
		{@render tabs('bottom', 'Bottom panel')}
		<div class="body">{@render panes('bottom')}</div>
	</section>
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
		gap: var(--sp-5);
		padding: var(--sp-2) var(--sp-5);
		height: 38px;
		flex-wrap: nowrap;
		overflow: hidden;
		background: var(--bg-raised);
		border-bottom: 1px solid var(--border);
	}
	.brand {
		display: inline-flex;
		align-items: center;
		gap: var(--sp-3);
		color: var(--accent);
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
		flex-wrap: wrap;
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
	.toggles {
		display: inline-flex;
		gap: var(--sp-2);
	}
	.icon-button[aria-pressed='true'] {
		color: var(--accent);
	}

	.middle {
		flex: 1;
		min-height: 0;
		display: flex;
		position: relative;
	}
	.rail {
		flex: none;
		width: 40px;
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: var(--sp-3);
		padding: var(--sp-4) 0;
		background: var(--bg-raised);
		border-right: 1px solid var(--border);
	}
	.rail-button {
		display: grid;
		place-items: center;
		width: 30px;
		height: 30px;
		padding: 0;
		border: 0;
		border-radius: var(--radius-control);
		background: transparent;
		color: var(--text-2);
	}
	.rail-button.current {
		background: var(--accent-soft);
		color: var(--accent);
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
