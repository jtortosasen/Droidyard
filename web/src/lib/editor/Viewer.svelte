<script lang="ts">
	import { untrack } from 'svelte';
	import FileQuestion from '@lucide/svelte/icons/file-question';
	import { copyToClipboard } from '../devices/copy';
	import { rowWindow } from '../logcat/virtual';
	import { formatSize, CAP, type FileViewer, type ViewerView } from './viewer';

	// The read-only viewer (plan m2d step 6): the lines of one file over the fixed-row
	// virtual list, with a line-number gutter. No highlighting. The text is drawn as text
	// nodes only, whatever the file holds.
	interface Props {
		viewer: FileViewer;
		/** What Copy path copies (the tree's absolute path). */
		copyPath: (path: string) => string;
	}
	let { viewer, copyPath }: Props = $props();

	const ROW = 18;
	const OVERSCAN = 10;

	let view = $state.raw<ViewerView>(untrack(() => viewer.view));
	let scrollTop = $state(0);
	let viewportHeight = $state(300);
	let copied = $state('');

	$effect(() => {
		const v = viewer;
		view = v.view;
		scrollTop = 0;
		copied = '';
		return v.subscribe(() => {
			view = v.view;
		});
	});

	const win = $derived(
		rowWindow({ scrollTop, viewportHeight, rowHeight: ROW, overscan: OVERSCAN, count: view.lines.length, following: false }),
	);
	const shown = $derived(view.lines.slice(win.start, win.end));
	const digits = $derived(String(Math.max(1, view.lines.length)).length);

	async function copy() {
		const r = await copyToClipboard(copyPath(view.path), typeof navigator === 'undefined' ? undefined : navigator.clipboard);
		copied = r.ok ? 'Copied' : r.reason;
	}
</script>

<div class="viewer">
	<div class="head">
		<span class="path" title={view.path}>{view.path}</span>
		{#if view.size !== null}<span class="size">{formatSize(view.size)}</span>{/if}
	</div>
	{#if view.state === 'deleted'}
		<p class="banner" role="status">This file was deleted from the project. The text below is the last version read.</p>
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
		<div
			class="scroll"
			style:--row="{ROW}px"
			style:--digits="{digits}ch"
			bind:clientHeight={viewportHeight}
			onscroll={(e) => (scrollTop = e.currentTarget.scrollTop)}
			tabindex="0"
			role="textbox"
			aria-readonly="true"
			aria-multiline="true"
			aria-label="File contents"
		>
			<div class="space" style:height="{win.totalHeight}px">
				<div class="window" style:top="{win.offset}px">
					{#each shown as line, i (win.start + i)}
						<div class="line"><span class="no" aria-hidden="true">{win.start + i + 1}</span><span class="text">{line}</span></div>
					{/each}
				</div>
			</div>
		</div>
		{#if view.capped}
			<p class="banner" role="status">
				Showing the first {formatSize(view.read)} of {view.size === null ? '' : formatSize(view.size)}; the viewer reads at most {formatSize(CAP)}.
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
	.scroll {
		flex: 1;
		min-height: 0;
		overflow: auto;
		font-family: var(--font-mono);
		font-size: var(--fs-mono);
		line-height: var(--row);
		background: var(--bg-panel);
		outline-offset: -2px;
	}
	.space {
		position: relative;
		min-width: 100%;
	}
	.window {
		position: absolute;
		left: 0;
		min-width: 100%;
		width: max-content;
	}
	.line {
		display: flex;
		height: var(--row);
		white-space: pre;
		tab-size: 4;
	}
	.no {
		flex: none;
		position: sticky;
		left: 0;
		box-sizing: content-box;
		width: var(--digits);
		padding: 0 var(--sp-4) 0 var(--sp-5);
		text-align: right;
		color: var(--text-3);
		background: var(--bg-panel);
		user-select: none;
	}
	.text {
		padding: 0 var(--sp-5) 0 var(--sp-4);
		color: var(--text);
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
