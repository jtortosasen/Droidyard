<script lang="ts">
	import Check from '@lucide/svelte/icons/check';
	import Copy from '@lucide/svelte/icons/copy';
	import RefreshCw from '@lucide/svelte/icons/refresh-cw';
	import type { UiTreeModel, UiTreeView } from './uitree';
	import { shortClass } from './uitree';
	import { attributesOf, copyForAgent, copyToClipboard, MAX_VALUE } from './copy';
	import { failureText } from '../build/failure';
	import { inert } from './text';

	// The UI tree of the selected device as an inspector (plan m2b step 6, m2d step 10): the
	// nodes as rows, the selected node's attributes below, and Copy for the agent. Every
	// node field is a text node: never markup, never a link, never a class or style. A row
	// selects the node here and hands its position to the parent, which asks the model for
	// the selector the actions below use.
	interface Props {
		model: UiTreeModel;
		onpick: (index: number) => void;
		connected: boolean;
	}
	let { model, onpick, connected }: Props = $props();

	let view = $state.raw<UiTreeView | null>(null);
	// The confirmation after Copy for the agent: shown for a moment, never silent.
	let copied = $state<'idle' | 'copied' | 'failed'>('idle');
	let copyReason = $state('');
	let timer: ReturnType<typeof setTimeout> | undefined;

	$effect(() => {
		const m = model;
		const read = () => {
			view = m.view;
		};
		read();
		return m.subscribe(read);
	});
	$effect(() => () => clearTimeout(timer));

	const selection = $derived(view?.selection ?? null);
	const detail = $derived(selection ? attributesOf(selection.node) : null);

	function pick(index: number) {
		model.select(index);
		onpick(index);
		copied = 'idle';
	}

	async function copy() {
		if (!selection) return;
		const out = await copyToClipboard(copyForAgent(selection.node, selection.snapshot), navigator.clipboard);
		copied = out.ok ? 'copied' : 'failed';
		copyReason = out.ok ? '' : out.reason;
		clearTimeout(timer);
		timer = setTimeout(() => (copied = 'idle'), 2500);
	}
</script>

<section class="tree">
	<div class="head">
		<h2>UI tree</h2>
		{#if view}
			<button
				type="button"
				class="btn-icon"
				disabled={!connected || view.device === '' || view.loading}
				onclick={() => void model.load()}
			>
				<RefreshCw size={12} /> Read the tree
			</button>
		{/if}
	</div>
	{#if !view}
		<p class="muted">Loading...</p>
	{:else}
		{#if view.device === ''}
			<p class="muted">Select a device first.</p>
		{:else if view.loading}
			<p class="muted">Reading...</p>
		{/if}
		{#if view.error}
			<p class="err">device.uiTree failed: {failureText(view.error)}</p>
		{/if}
		{#each view.notes as note, i (i)}
			<p class="muted note">{note}</p>
		{/each}
		{#if view.rows.length > 0}
			<div class="list" role="group" aria-label="UI tree nodes">
				{#each view.rows as row (row.index)}
					<button
						type="button"
						class="row"
						class:selected={view.selected === row.index}
						aria-pressed={view.selected === row.index}
						onclick={() => pick(row.index)}
					>
						<span class="indent">{row.indent}</span>
						<span class="cls">{shortClass(row.class)}</span>
						{#if row.id !== ''}<span class="id">{row.id}</span>{/if}
						{#if row.text !== ''}<span class="txt">{row.text}</span>{/if}
						{#if row.desc !== ''}<span class="muted">{row.desc}</span>{/if}
						{#if row.flags !== ''}<span class="flags">{row.flags}</span>{/if}
					</button>
				{/each}
			</div>
			<div class="details" aria-label="Selected node">
				{#if selection && detail}
					<div class="head">
						<h3>{shortClass(inert(selection.node.class ?? 'Node'))}</h3>
						<button type="button" class:btn-primary={copied !== 'copied'} class="btn-icon" onclick={() => void copy()}>
							{#if copied === 'copied'}
								<Check size={12} /> Copied
							{:else}
								<Copy size={12} /> Copy for the agent
							{/if}
						</button>
					</div>
					<p class="status" role="status">
						{#if copied === 'copied'}
							Copied to the clipboard.
						{:else if copied === 'failed'}
							<span class="err">Copy failed: {copyReason}</span>
						{/if}
					</p>
					<dl>
						{#each detail.attributes as a, i (i)}
							<dt>{a.name}</dt>
							<dd>{a.quote ? inert(a.value) : a.value}{a.cut ? ` (cut to ${MAX_VALUE} characters)` : ''}</dd>
						{/each}
					</dl>
					{#if detail.omitted > 0}
						<p class="muted">{detail.omitted} more attributes not shown.</p>
					{/if}
				{:else}
					<p class="muted">Select a node to inspect it. Tap and type below act on it.</p>
				{/if}
			</div>
		{:else if view.loaded && !view.error}
			<p class="muted">The tree is empty.</p>
		{/if}
	{/if}
</section>

<style>
	.head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--sp-3);
	}
	.head :global(h2),
	.head h3 {
		margin: 0;
	}
	.note {
		margin: var(--sp-1) 0;
		overflow-wrap: anywhere;
	}
	.list {
		margin-top: var(--sp-3);
		max-height: 260px;
		overflow: auto;
		padding: var(--sp-1);
		border: 1px solid var(--border);
		border-radius: var(--radius-control);
		background: var(--bg-window);
	}
	.row {
		display: flex;
		align-items: baseline;
		gap: var(--sp-3);
		width: 100%;
		min-height: var(--row);
		padding: 0 var(--sp-3);
		border: 0;
		border-radius: var(--radius-control);
		background: transparent;
		text-align: left;
		font-family: var(--font-mono);
		font-size: var(--fs-mono);
		overflow: hidden;
		white-space: nowrap;
	}
	.row:hover {
		background: var(--bg-hover);
	}
	.row.selected {
		background: var(--accent-soft);
		box-shadow: var(--sel-edge);
	}
	.indent {
		flex: none;
		white-space: pre;
	}
	.cls {
		flex: none;
		color: var(--accent);
	}
	.id,
	.txt {
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.txt::before,
	.txt::after {
		content: '"';
	}
	.flags {
		margin-left: auto;
		color: var(--text-3);
		font-size: var(--fs-small);
	}
	.details {
		margin-top: var(--sp-4);
		padding: var(--sp-4);
		border: 1px solid var(--border);
		border-radius: var(--radius-control);
		background: var(--bg-raised);
	}
	.status {
		min-height: 1.4em;
		margin: var(--sp-2) 0;
		color: var(--success);
	}
	dl {
		display: grid;
		grid-template-columns: max-content 1fr;
		gap: var(--sp-1) var(--sp-4);
		margin: 0;
	}
	dt {
		color: var(--text-2);
		font-size: var(--fs-small);
	}
	dd {
		margin: 0;
		font-family: var(--font-mono);
		font-size: var(--fs-mono);
		overflow-wrap: anywhere;
		white-space: pre-wrap;
	}
</style>
