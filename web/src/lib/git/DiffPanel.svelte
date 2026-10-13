<script lang="ts">
	import { untrack } from 'svelte';
	import type { Attachment } from 'svelte/attachments';
	import { LazyEditor, loadEditor, type CompareOptions, type EditorOptions, type ReviewOptions } from '../editor/surface';
	import type { TabsModel } from '../editor/tabs';
	import type { EditorModel, ViewerSet, ViewerView } from '../editor/viewer';
	import type { HeadBase, BaseView } from './base';
	import { LETTERS, MARK_WORDS } from './changes';
	import { loadDiffMode, saveDiffMode, type DiffMode } from './diffmode';
	import { commitHeader, NO_PARENT, shortOid, type CommitFile, type CommitText, type CommitTextView, type DiffSource, type SourceView } from './diffsource';
	import { browserStorage } from '../ui/layout';
	import type { GitView, GitWatch } from './watch';

	// The Diff tab (plan m2l step 5; ADR-0067 §1-§3): the Editor's active file against HEAD, in
	// Classic (the merge view: HEAD read-only, the buffer editable) or Review (the buffer on the
	// left, HEAD's rows on the right; editor/cm/review.ts), by a toggle remembered per browser. The
	// buffer is the Editor tab's own, so the two tabs share one document, and the side follows it
	// (editor/cm/merge.ts). A deleted file has no buffer: Review's right pane shows HEAD's text alone,
	// all red, and the toggle is disabled.
	// HEAD's text comes from `git.read` and is read again when HEAD or the file's entry changes.
	// A binary or too-large file, and no repository, are a message. Text from files and from git reaches the page as text nodes only.
	// A second source (plan m2n step 9; ADR-0070 §4): a changed file of a commit, picked in the History
	// tab, its text at the commit against its text at the commit's first parent, both read-only (no
	// buffer, nothing mirrored or followed). Project returns the source to the working tree when the
	// Editor's active file changes and when a file is opened from the Editor or the Git tree.
	interface Props {
		tabs: TabsModel;
		viewers: ViewerSet;
		git: GitWatch;
		base: HeadBase;
		source: DiffSource;
		commitText: CommitText;
	}
	let { tabs, viewers, git, base, source, commitText }: Props = $props();

	let active = $state<string | null>(untrack(() => tabs.view.active));
	let gview = $state.raw<GitView>(untrack(() => git.view));
	let bview = $state.raw<BaseView>(untrack(() => base.view));
	let vview = $state.raw<ViewerView | null>(null);
	let sview = $state.raw<SourceView>(untrack(() => source.view));
	let cview = $state.raw<CommitTextView>(untrack(() => commitText.view));
	let editorError = $state('');
	const storage = browserStorage(window);
	let mode = $state<DiffMode>(loadDiffMode(storage));
	const choose = (m: DiffMode) => {
		mode = m;
		saveDiffMode(storage, m);
	};

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
		const s = source;
		sview = s.view;
		return s.subscribe(() => {
			sview = s.view;
		});
	});
	$effect(() => {
		const c = commitText;
		cview = c.view;
		return c.subscribe(() => {
			cview = c.view;
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
	// Gone from the working tree, by the status or by the file's own read.
	const deleted = $derived(entry?.mark === 'deleted' || vview?.state === 'deleted');

	// Why there is nothing to compare yet, as text; null when the two texts can be compared.
	const blocked = $derived.by((): string | null => {
		if (active === null) return 'Open a file in the Editor tab to see it against HEAD.';
		if ((gview.state === 'error' || gview.state === 'ended') && gview.problem) return gview.problem.text;
		if (gview.head === null) return gview.state === 'paused' ? 'Paused while the page is hidden.' : 'Reading the status…';
		if (deleted) return null;
		if (vview === null || vview.state === 'loading') return 'Reading the file…';
		if (vview.state === 'binary') return 'This file is binary; there is no text to compare.';
		if (vview.state === 'error') return vview.error ?? 'The file could not be read.';
		if (buffer === null) return 'There is no text to compare.';
		return null;
	});

	const commitFile = $derived<CommitFile | null>(sview.kind === 'commit' ? sview.file : null);

	// HEAD's text follows the file, its entry and HEAD's commit; nothing is read while something else blocks,
	// or while a commit's file is shown (its two texts are read by `commitText`).
	$effect(() => {
		const path = active;
		const mark = entry?.mark;
		const origPath = entry?.origPath;
		const id = oid;
		const stop = blocked !== null || commitFile !== null;
		untrack(() => {
			if (stop || path === null) base.follow(null);
			else base.follow({ path, mark, origPath, oid: id });
		});
	});
	$effect(() => {
		const file = commitFile;
		untrack(() => commitText.follow(file));
	});

	const worktreeShown = $derived(blocked ?? (bview.kind === 'message' ? bview.text : bview.kind === 'text' ? null : 'Reading HEAD…'));
	const commitShown = $derived(cview.kind === 'message' ? cview.text : cview.kind === 'text' ? null : 'Reading the file at the commit…');
	const shown = $derived(commitFile !== null ? commitShown : worktreeShown);
	// The right pane alone: a file gone from the working tree, or deleted by the commit.
	const alone = $derived(commitFile !== null ? commitFile.status === 'deleted' : deleted);
	const headText = $derived(bview.kind === 'text' ? bview.text : null);
	const readOnly = $derived(vview !== null && vview.readOnly !== null);
	// What the view is made of; it is made again when any of it changes.
	type Made =
		| { kind: 'deleted'; text: string; path: string; caption?: string }
		| { kind: DiffMode; text: string; buffer: NonNullable<typeof buffer>; viewer: EditorModel; readOnly: boolean }
		| { kind: 'commit'; mode: DiffMode; file: CommitFile; before: string; after: string };
	const mount = $derived.by((): Made | null => {
		if (shown !== null) return null;
		if (commitFile !== null) {
			if (cview.kind !== 'text') return null;
			if (cview.after === null) {
				const caption = `${commitFile.parent === null ? NO_PARENT : shortOid(commitFile.parent)} (deleted by ${shortOid(commitFile.oid)})`;
				return { kind: 'deleted', text: cview.before, path: commitFile.path, caption };
			}
			return { kind: 'commit', mode, file: commitFile, before: cview.before, after: cview.after };
		}
		if (headText === null || active === null) return null;
		if (deleted) return { kind: 'deleted', text: headText, path: active };
		if (buffer === null || viewer === null) return null;
		return { kind: mode, text: headText, buffer, viewer, readOnly };
	});

	const view: Attachment<HTMLElement> = (host) => {
		editorError = '';
		const m = untrack(() => mount);
		if (m === null) return;
		const onError = (e: unknown) => {
			editorError = e instanceof Error ? e.message : String(e);
		};
		if (m.kind === 'commit') {
			// Two read-only texts: the commit's file against its first parent's. No buffer is given, so none can be touched.
			const parent = m.file.parent === null ? NO_PARENT : shortOid(m.file.parent);
			const now = shortOid(m.file.oid);
			if (m.mode === 'classic') {
				const ed = new LazyEditor<CompareOptions>(
					host,
					{
						disk: m.before,
						text: { text: m.after, path: m.file.path },
						labels: { a: parent, b: now },
						sideLabel: `File contents at ${parent}`,
						textLabel: `File contents at ${now}`,
					},
					onError,
					loadEditor,
					(mod, h, o) => mod.mountCompare(h, o),
				);
				return () => ed.destroy();
			}
			const opts: ReviewOptions = {
				buffer: null,
				text: { text: m.after, path: m.file.path },
				base: m.before,
				path: m.file.path,
				labels: { left: now, right: `Against ${parent}` },
				onSave: () => {},
			};
			const ed = new LazyEditor<ReviewOptions>(host, opts, onError, loadEditor, (mod, h, o) => mod.mountReview(h, o));
			return () => ed.destroy();
		}
		if (m.kind === 'classic') {
			const ed = new LazyEditor<EditorOptions>(host, { buffer: m.buffer, onSave: () => void m.viewer.save() }, onError, loadEditor, (mod, h, o) =>
				mod.mountCompare(h, { ...o, disk: m.text, labels: { a: 'HEAD', b: 'Working tree' }, sideLabel: 'File contents at HEAD' }),
			);
			return () => ed.destroy();
		}
		const opts: ReviewOptions =
			m.kind === 'deleted'
				? { buffer: null, base: m.text, path: m.path, onSave: () => {}, ...(m.caption !== undefined ? { labels: { left: '', right: m.caption } } : {}) }
				: { buffer: m.buffer, base: m.text, path: untrack(() => active) ?? undefined, onSave: () => void m.viewer.save() };
		const ed = new LazyEditor<ReviewOptions>(host, opts, onError, loadEditor, (mod, h, o) => mod.mountReview(h, o));
		return () => ed.destroy();
	};
</script>

{#snippet modes()}
	<div class="modes" role="group" aria-label="Diff mode">
		{#each ['classic', 'review'] as const as m (m)}
			<button type="button" data-mode={m} aria-pressed={(alone ? 'review' : mode) === m} disabled={alone} onclick={() => choose(m)}>
				{m === 'classic' ? 'Classic' : 'Review'}
			</button>
		{/each}
	</div>
{/snippet}

<div class="diff">
	{#if commitFile !== null}
		<div class="head" data-diff-source="commit" data-commit={commitFile.oid} data-parent={commitFile.parent ?? undefined} data-diff-path={commitFile.path}>
			<span class="path" title={commitFile.path}>{commitHeader(commitFile)}</span>
			<span class="mark {commitFile.status}" title={MARK_WORDS[commitFile.status]}>{LETTERS[commitFile.status]}</span>
			<span class="word">{MARK_WORDS[commitFile.status]}</span>
			<span class="vs">read-only</span>
			{@render modes()}
		</div>
	{:else if active !== null}
		<div class="head" data-diff-source="worktree" data-diff-path={active}>
			<span class="path" title={active}>{active}</span>
			{#if entry}
				<span class="mark {entry.mark}" title={MARK_WORDS[entry.mark]}>{LETTERS[entry.mark]}</span>
				<span class="word">{MARK_WORDS[entry.mark]}</span>
			{/if}
			<span class="vs">vs HEAD</span>
			{@render modes()}
		</div>
	{/if}
	{#if shown !== null}
		<p class="message" role="status">{shown}</p>
	{:else if mount}
		{#key mount}
			<div class="host" data-diff={mount.kind} {@attach view}></div>
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
	.modes {
		flex: none;
		display: flex;
	}
	.modes button {
		font: inherit;
		padding: 0 var(--sp-4);
		color: var(--text-2);
		background: var(--bg-panel);
		border: 1px solid var(--border);
		cursor: pointer;
	}
	.modes button:first-child {
		border-radius: var(--radius-control) 0 0 var(--radius-control);
	}
	.modes button:last-child {
		border-radius: 0 var(--radius-control) var(--radius-control) 0;
		border-left: 0;
	}
	.modes button[aria-pressed='true'] {
		color: var(--text);
		background: var(--bg-active);
	}
	.modes button:disabled {
		cursor: default;
		opacity: 0.6;
	}
	/* The diff view's host: a flex column its shadow root's view fills. */
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
