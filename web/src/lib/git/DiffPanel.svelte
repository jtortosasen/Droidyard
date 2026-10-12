<script lang="ts">
	import { untrack } from 'svelte';
	import type { Attachment } from 'svelte/attachments';
	import { LazyEditor, loadEditor } from '../editor/surface';
	import type { TabsModel } from '../editor/tabs';
	import type { EditorModel, ViewerSet, ViewerView } from '../editor/viewer';
	import type { HeadBase, BaseView } from './base';
	import { LETTERS, MARK_WORDS } from './changes';
	import type { GitView, GitWatch } from './watch';

	// The Diff tab (plan m2l step 5; ADR-0067 §1-§3): the Editor's active file against HEAD, in
	// Classic (the merge view: HEAD read-only, the buffer editable). The buffer is the Editor
	// tab's own, so the two tabs share one document, and the side follows it (editor/cm/merge.ts).
	// HEAD's text comes from `git.read` and is read again when HEAD or the file's entry changes.
	// A deleted, binary or too-large file, and no repository, are a message. Review arrives with
	// step 7. Text from files and from git reaches the page as text nodes only.
	interface Props {
		tabs: TabsModel;
		viewers: ViewerSet;
		git: GitWatch;
		base: HeadBase;
	}
	let { tabs, viewers, git, base }: Props = $props();

	let active = $state<string | null>(untrack(() => tabs.view.active));
	let gview = $state.raw<GitView>(untrack(() => git.view));
	let bview = $state.raw<BaseView>(untrack(() => base.view));
	let vview = $state.raw<ViewerView | null>(null);
	let editorError = $state('');

	$effect(() => {
		const t = tabs;
		active = t.view.active;
		return t.subscribe(() => {
			active = t.view.active;
		});
	});
	$effect(() => {
		const g = git;
		gview = g.view;
		return g.subscribe(() => {
			gview = g.view;
		});
	});
	$effect(() => {
		const b = base;
		bview = b.view;
		return b.subscribe(() => {
			bview = b.view;
		});
	});

	// The active file's model (it exists while its tab does) and its view.
	const viewer = $derived<EditorModel | null>(active === null ? null : viewers.get(active));
	$effect(() => {
		const v = viewer;
		if (v === null) {
			vview = null;
			return;
		}
		vview = v.view;
		return v.subscribe(() => {
			vview = v.view;
		});
	});

	const entry = $derived(active === null ? undefined : gview.entries.find((e) => e.path === active));
	const oid = $derived(gview.head?.oid);
	const buffer = $derived(vview?.buffer ?? null);

	// Why there is nothing to compare yet, as text; null when the two texts can be compared.
	const blocked = $derived.by((): string | null => {
		if (active === null) return 'Open a file in the Editor tab to see it against HEAD.';
		if ((gview.state === 'error' || gview.state === 'ended') && gview.problem) return gview.problem.text;
		if (gview.head === null) return gview.state === 'paused' ? 'Paused while the page is hidden.' : 'Reading the status…';
		if (vview === null || vview.state === 'loading') return 'Reading the file…';
		if (entry?.mark === 'deleted' || vview.state === 'deleted') return 'This file is deleted from the working tree, so there is no text to put beside HEAD’s.';
		if (vview.state === 'binary') return 'This file is binary; there is no text to compare.';
		if (vview.state === 'error') return vview.error ?? 'The file could not be read.';
		if (buffer === null) return 'There is no text to compare.';
		return null;
	});

	// HEAD's text follows the file, its entry and HEAD's commit; nothing is read while something else blocks.
	$effect(() => {
		const path = active;
		const mark = entry?.mark;
		const origPath = entry?.origPath;
		const id = oid;
		const stop = blocked !== null;
		untrack(() => {
			if (stop || path === null) base.follow(null);
			else base.follow({ path, mark, origPath, oid: id });
		});
	});

	const shown = $derived(blocked ?? (bview.kind === 'message' ? bview.text : bview.kind === 'text' ? null : 'Reading HEAD…'));
	const headText = $derived(bview.kind === 'text' ? bview.text : null);
	const readOnly = $derived(vview !== null && vview.readOnly !== null);
	// What the compare view is made of; it is made again when any of it changes.
	const mount = $derived(shown === null && buffer !== null && headText !== null && viewer !== null ? { buffer, viewer, text: headText, readOnly } : null);

	const compare: Attachment<HTMLElement> = (host) => {
		editorError = '';
		const m = untrack(() => mount);
		if (m === null) return;
		const ed = new LazyEditor(
			host,
			{ buffer: m.buffer, onSave: () => void m.viewer.save() },
			(e) => {
				editorError = e instanceof Error ? e.message : String(e);
			},
			loadEditor,
			(mod, h, o) => mod.mountCompare(h, { ...o, disk: m.text, labels: { a: 'HEAD', b: 'Working tree' }, sideLabel: 'File contents at HEAD' }),
		);
		return () => ed.destroy();
	};
</script>

<div class="diff">
	{#if active !== null}
		<div class="head">
			<span class="path" title={active}>{active}</span>
			{#if entry}
				<span class="mark {entry.mark}" title={MARK_WORDS[entry.mark]}>{LETTERS[entry.mark]}</span>
				<span class="word">{MARK_WORDS[entry.mark]}</span>
			{/if}
			<span class="vs">vs HEAD</span>
		</div>
	{/if}
	{#if shown !== null}
		<p class="message" role="status">{shown}</p>
	{:else if mount}
		{#key mount}
			<div class="host" {@attach compare}></div>
		{/key}
	{/if}
	{#if editorError}
		<p class="error" role="alert">The diff could not be shown: {editorError}</p>
	{/if}
</div>

<style>
	/* As the Editor tab: the shell's pane pads and scrolls its content, so this fills the centre's body instead. */
	.diff {
		position: absolute;
		inset: 0;
		display: flex;
		flex-direction: column;
		background: var(--bg-panel);
	}
	.head {
		flex: none;
		display: flex;
		align-items: baseline;
		gap: var(--sp-4);
		padding: var(--sp-2) var(--sp-5);
		border-bottom: 1px solid var(--border);
		font-size: var(--fs-small);
		color: var(--text-2);
	}
	.path {
		flex: 0 1 auto;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		color: var(--text);
	}
	.mark {
		flex: none;
		font-family: var(--font-mono);
	}
	.mark.modified {
		color: var(--warning);
	}
	.mark.added,
	.mark.untracked {
		color: var(--success);
	}
	.mark.deleted,
	.mark.conflicted {
		color: var(--danger);
	}
	.mark.renamed {
		color: var(--accent);
	}
	.vs {
		margin-left: auto;
		flex: none;
		color: var(--text-3);
	}
	/* The compare view's host: a flex column its shadow root's view fills. */
	.host {
		flex: 1;
		min-height: 0;
		min-width: 0;
		display: flex;
		flex-direction: column;
	}
	.message {
		margin: 0;
		padding: var(--sp-5);
		color: var(--text-2);
		overflow-wrap: anywhere;
	}
	.error {
		flex: none;
		margin: 0;
		padding: var(--sp-3) var(--sp-5);
		font-size: var(--fs-small);
		color: var(--danger);
		background: var(--bg-raised);
	}
</style>
