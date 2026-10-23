<script lang="ts">
	import { tick, untrack, type Component } from 'svelte';
	import Command from '@lucide/svelte/icons/command';
	import File from '@lucide/svelte/icons/file';
	import FileCode from '@lucide/svelte/icons/file-code';
	import FileJson from '@lucide/svelte/icons/file-json';
	import FileText from '@lucide/svelte/icons/file-text';
	import Folder from '@lucide/svelte/icons/folder';
	import FolderOpen from '@lucide/svelte/icons/folder-open';
	import Image from '@lucide/svelte/icons/image';
	import Package from '@lucide/svelte/icons/package';
	import Search from '@lucide/svelte/icons/search';
	import type { IconKind } from '../files/tree';
	import type { Command as PaletteCommand, PaletteModel, PaletteView, Segment } from './palette';

	// The palette (plan m2d step 9): a modal <dialog> near the top, an input, the results,
	// the hints. ⌘P opens the files and ⇧⌘P the commands, from anywhere on the page; Escape
	// closes the dialog and the browser returns focus to where it was. File names and
	// commands are drawn as text nodes, the matched characters in <mark>-like spans.
	interface Props {
		model: PaletteModel;
		/** The command registry, read each time the palette is searched. */
		commands: () => PaletteCommand[];
		/** A file was chosen. */
		onopen: (path: string) => void;
	}
	let { model, commands, onopen }: Props = $props();

	$effect(() => model.bind(commands, onopen));

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

	let view = $state.raw<PaletteView>(untrack(() => model.view));
	let dialog = $state<HTMLDialogElement>();
	let input = $state<HTMLInputElement>();
	let list = $state<HTMLUListElement>();

	$effect(() => {
		const m = model;
		untrack(() => (view = m.view));
		return m.subscribe(() => (view = m.view));
	});

	// The model decides whether the palette is open; the dialog follows.
	$effect(() => {
		const open = view.open;
		if (!dialog) return;
		if (open && !dialog.open) {
			dialog.showModal();
			void tick().then(() => {
				input?.focus();
				input?.select();
			});
		} else if (!open && dialog.open) {
			dialog.close();
		}
	});

	$effect(() => {
		const i = view.selected;
		void tick().then(() => list?.children[i]?.scrollIntoView({ block: 'nearest' }));
	});

	function onwindowkey(e: KeyboardEvent) {
		if (!(e.metaKey || e.ctrlKey) || e.altKey || e.key.toLowerCase() !== 'p') return;
		e.preventDefault();
		model.show(e.shiftKey ? 'commands' : 'files');
	}

	function onkeydown(e: KeyboardEvent) {
		if (e.isComposing) return;
		if (e.key === 'ArrowDown') {
			e.preventDefault();
			model.move(1);
		} else if (e.key === 'ArrowUp') {
			e.preventDefault();
			model.move(-1);
		} else if (e.key === 'Enter') {
			e.preventDefault();
			model.choose();
		}
	}

	function onbackdrop(e: MouseEvent) {
		if (e.target === dialog) model.close();
	}

	const placeholder = $derived(view.mode === 'commands' ? 'Type a command' : 'Search files by name (type > for commands)');
	const hint = $derived(view.mode === 'commands' ? 'run' : 'open');
</script>

{#snippet marked(parts: readonly Segment[])}
	{#each parts as s, i (i)}{#if s.hit}<span class="hit">{s.text}</span>{:else}{s.text}{/if}{/each}
{/snippet}

<svelte:window onkeydown={onwindowkey} />

<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_noninteractive_element_interactions -->
<dialog bind:this={dialog} aria-label="Command palette" onclose={() => model.close()} onclick={onbackdrop}>
	<div class="search">
		<Search size={16} />
		<input
			bind:this={input}
			type="text"
			role="combobox"
			aria-expanded="true"
			aria-controls="palette-list"
			aria-activedescendant={view.items.length > 0 ? `palette-${view.selected}` : undefined}
			aria-label={view.mode === 'commands' ? 'Command' : 'File'}
			{placeholder}
			spellcheck="false"
			autocomplete="off"
			value={view.query}
			oninput={(e) => model.setQuery(e.currentTarget.value)}
			{onkeydown}
		/>
	</div>
	<ul id="palette-list" role="listbox" bind:this={list}>
		{#each view.items as item, i (item.kind === 'file' ? item.path : item.command.id)}
			{@const disabled = item.kind === 'command' && item.command.disabled !== null}
			<!-- svelte-ignore a11y_click_events_have_key_events -->
			<li
				id="palette-{i}"
				role="option"
				aria-selected={i === view.selected}
				aria-disabled={disabled}
				class:current={i === view.selected}
				class:disabled
				onpointermove={() => model.select(i)}
				onclick={() => model.choose(i)}
			>
				{#if item.kind === 'file'}
					{@const Icon = ICONS[item.icon]}
					<span class="icon"><Icon size={16} /></span>
					<span class="name">{@render marked(item.name)}</span>
					<span class="dir">{item.dir}</span>
				{:else}
					<span class="icon"><Command size={16} /></span>
					<span class="name">{@render marked(item.title)}</span>
					{#if item.command.disabled !== null}<span class="dir">{item.command.disabled}</span>{/if}
				{/if}
			</li>
		{:else}
			<li class="none" role="presentation">{view.query.trim() === '' ? 'Nothing to list yet' : 'No match'}</li>
		{/each}
	</ul>
	<footer>
		<span><kbd>↑</kbd><kbd>↓</kbd> to move</span>
		<span><kbd>↵</kbd> to {hint}</span>
		<span><kbd>&gt;</kbd> for commands</span>
		{#if view.partial}<span class="partial">The project is large: the list is partial</span>{/if}
	</footer>
</dialog>

<style>
	dialog {
		position: fixed;
		inset: 12vh auto auto 50%;
		translate: -50% 0;
		margin: 0;
		padding: 0;
		width: min(640px, 92vw);
		max-height: 70vh;
		flex-direction: column;
		color: var(--text);
		background: var(--bg-raised);
		border: 1px solid var(--border);
		border-radius: var(--radius-panel);
		box-shadow: var(--shadow);
		overflow: hidden;
	}
	dialog[open] {
		display: flex;
	}
	dialog::backdrop {
		background: var(--scrim);
	}
	.search {
		display: flex;
		align-items: center;
		gap: var(--sp-4);
		padding: var(--sp-5) var(--sp-6);
		border-bottom: 1px solid var(--border);
		color: var(--text-2);
	}
	input {
		flex: 1;
		min-width: 0;
		font: inherit;
		font-size: var(--fs-large);
		color: var(--text);
		background: transparent;
		border: 0;
		outline: 0;
	}
	input::placeholder {
		color: var(--text-3);
	}
	ul {
		list-style: none;
		margin: 0;
		padding: var(--sp-2);
		overflow-y: auto;
		flex: 1;
		min-height: 0;
	}
	li {
		display: flex;
		align-items: center;
		gap: var(--sp-4);
		min-height: calc(var(--row) + 6px);
		padding: 0 var(--sp-4);
		border-radius: var(--radius-control);
		cursor: default;
		white-space: nowrap;
	}
	li.current {
		background: var(--accent-soft);
		box-shadow: var(--sel-edge);
	}
	li.disabled .name {
		color: var(--text-3);
	}
	li.none {
		color: var(--text-3);
		justify-content: center;
	}
	.icon {
		display: inline-flex;
		flex: none;
		color: var(--text-2);
	}
	.name {
		font-weight: 600;
		flex: none;
		max-width: 60%;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.hit {
		color: var(--accent);
	}
	.dir {
		flex: 1;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		color: var(--text-2);
		font-family: var(--font-mono);
		font-size: var(--fs-small);
	}
	footer {
		display: flex;
		flex-wrap: wrap;
		gap: var(--sp-6);
		padding: var(--sp-3) var(--sp-6);
		border-top: 1px solid var(--border);
		color: var(--text-3);
		font-size: var(--fs-small);
	}
	kbd {
		font: inherit;
		font-family: var(--font-mono);
		margin-right: var(--sp-2);
		padding: 0 var(--sp-2);
		border: 1px solid var(--border);
		border-radius: var(--radius-control);
		color: var(--text-2);
	}
	.partial {
		margin-left: auto;
		color: var(--warning);
	}
</style>
