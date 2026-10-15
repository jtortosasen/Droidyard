<script lang="ts">
	import ChevronDown from '@lucide/svelte/icons/chevron-down';
	import ChevronRight from '@lucide/svelte/icons/chevron-right';
	import ListIcon from '@lucide/svelte/icons/list';
	import ListTree from '@lucide/svelte/icons/list-tree';
	import Search from '@lucide/svelte/icons/search';
	import X from '@lucide/svelte/icons/x';
	import { commitFileOf, type CommitFile } from './diffsource';
	import { fullDate, listRows, treeRows } from './files';
	import type { ExpandedCommit } from './history';

	// The commit opened under its row in the History tab (plan m2n step 8; item 10), across the
	// table's width. Left: Commit, Parents (a click selects the parent), Author, Date (full, with
	// the zone), Committer, a blank line, the message as preformatted text. Right: the files the
	// commit changed under the project root, as a folder tree or a flat list, each with its counts.
	// A narrow strip at the right holds × and the view buttons (the second is shown disabled:
	// its function is unknown, item 10). Every text from git is a text node; the message is a <pre>.
	interface Props {
		expanded: ExpandedCommit;
		/** The panel's height, in px: the table's row below it moves down by exactly this. */
		height: number;
		onclose: () => void;
		onparent: (oid: string) => void;
		onretry: () => void;
		/** A changed file was picked: it opens in the Diff tab against the commit's first parent (plan m2n step 9). */
		onopenfile?: (file: CommitFile) => void;
	}
	let { expanded, height, onclose, onparent, onretry, onopenfile }: Props = $props();

	let mode = $state<'tree' | 'list'>('tree');
	let collapsed = $state.raw<ReadonlySet<string>>(new Set());
	const show = $derived(expanded.show);
	const rows = $derived(show === null ? [] : mode === 'tree' ? treeRows(show.files, collapsed) : listRows(show.files));

	function openFile(r: { path: string; origPath?: string; status: 'added' | 'modified' | 'deleted' | 'renamed'; binary: boolean }) {
		if (show !== null) onopenfile?.(commitFileOf(show, r, expanded.root));
	}

	function toggleDir(path: string) {
		const next = new Set(collapsed);
		if (!next.delete(path)) next.add(path);
		collapsed = next;
	}
</script>

<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
<!-- Keys typed in the text must not reach the table's own key handling. -->
<div
	class="view"
	style:height="{height}px"
	data-commit-view
	data-oid={expanded.oid}
	data-state={expanded.state}
	role="region"
	aria-label="Commit {expanded.oid.slice(0, 7)}"
	tabindex="-1"
	onkeydown={(e) => {
		// Arrow keys here belong to the text; only Escape leaves.
		if (e.key === 'Escape') onclose();
		e.stopPropagation();
	}}
>
	{#if expanded.state === 'loading'}
		<p class="note" role="status">Reading the commit…</p>
	{:else if expanded.state === 'error' || show === null}
		<div class="note problem" role="alert">
			<p>{expanded.problem ?? 'The commit could not be read.'}</p>
			<button type="button" onclick={onretry}>Retry</button>
		</div>
	{:else}
		<div class="left">
			<dl>
				<dt>Commit</dt>
				<dd class="mono" data-field="commit">{show.oid}</dd>
				<dt>Parents</dt>
				<dd class="mono" data-field="parents">
					{#each show.parents as p (p)}
						<button type="button" class="link" data-parent={p} title="Select this parent" onclick={() => onparent(p)}>{p}</button>
					{:else}
						<span class="none">none (a root commit)</span>
					{/each}
				</dd>
				<dt>Author</dt>
				<dd data-field="author"><span class="t">{show.author.name} &lt;{show.author.email}&gt;</span></dd>
				<dt>Date</dt>
				<dd data-field="date"><span class="t">{fullDate(show.author.time)}</span></dd>
				<dt>Committer</dt>
				<dd data-field="committer">
					<span class="t">{show.committer.name} &lt;{show.committer.email}&gt;{show.committer.time === show.author.time ? '' : `, ${fullDate(show.committer.time)}`}</span>
				</dd>
			</dl>
			<pre class="message" data-field="message">{show.message}</pre>
			{#if show.messageTruncated}
				<p class="warn" data-message-truncated>The message is longer than shown; the end is left out.</p>
			{/if}
		</div>
		<div class="right">
			{#if rows.length === 0 && !show.truncated}
				<p class="none" role="status" data-files-empty>No file of this commit is under the workspace.</p>
			{:else}
				<div class="files" role={mode === 'tree' ? 'tree' : 'list'} aria-label="Changed files" data-files-view={mode}>
					{#each rows as r (r.key)}
						{#if r.kind === 'dir'}
							<button
								type="button"
								class="dir"
								role="treeitem"
								aria-selected="false"
								aria-expanded={!r.collapsed}
								data-dir={r.path}
								style:padding-left="calc(var(--sp-3) + {r.depth - 1} * var(--sp-5))"
								title={r.path}
								onclick={() => toggleDir(r.path)}
							>
								{#if r.collapsed}<ChevronRight size={13} />{:else}<ChevronDown size={13} />{/if}
								<span class="t">{r.label}</span>
							</button>
						{:else}
							<!-- The whole row takes a click; the name inside is the button the keyboard reaches (its click bubbles here). -->
							<div
								class="file {r.status}"
								role={mode === 'tree' ? 'treeitem' : 'listitem'}
								aria-selected={mode === 'tree' ? false : undefined}
								data-file={r.path}
								data-status={r.status}
								data-added={r.added}
								data-removed={r.removed}
								data-binary={r.binary}
								onclick={() => openFile(r)}
								style:padding-left={mode === 'tree' ? `calc(var(--sp-3) + ${r.depth - 1} * var(--sp-5) + 13px + var(--sp-3))` : 'var(--sp-3)'}
								title={r.path}
							>
								<button type="button" class="name t" data-open title="Open in the Diff tab: {r.path}">{r.label}</button>
								{#if r.dir !== ''}<span class="dirname t">{r.dir}</span>{/if}
								{#if r.origPath !== undefined}<span class="orig t" data-orig>from {r.origPath}</span>{/if}
								{#if r.counts !== ''}<span class="counts" class:binary={r.binary}>{r.counts}</span>{/if}
							</div>
						{/if}
					{/each}
				</div>
				{#if show.truncated}
					<p class="warn" data-files-truncated>The commit changed more files than are listed.</p>
				{/if}
			{/if}
		</div>
	{/if}
	<div class="strip">
		<button type="button" class="icon" title="Close" aria-label="Close the commit" data-close onclick={onclose}><X size={14} /></button>
		<button
			type="button"
			class="icon"
			title={mode === 'tree' ? 'Show the files as a list' : 'Show the files as a tree'}
			aria-label={mode === 'tree' ? 'Show the files as a list' : 'Show the files as a tree'}
			data-files-mode={mode}
			onclick={() => (mode = mode === 'tree' ? 'list' : 'tree')}
		>
			{#if mode === 'tree'}<ListIcon size={14} />{:else}<ListTree size={14} />{/if}
		</button>
		<button type="button" class="icon" disabled title="Not available yet" aria-label="Not available yet" data-strip-extra><Search size={14} /></button>
	</div>
</div>

<style>
	.view {
		box-sizing: border-box;
		display: grid;
		grid-template-columns: minmax(0, 1fr) minmax(0, 1fr) 30px;
		/* One row as tall as the panel, so each column scrolls inside it instead of growing past it. */
		grid-template-rows: minmax(0, 1fr);
		overflow: hidden;
		/* A wheel or touch scroll that reaches the panel's end does not carry on into the log behind it (plan m2n R1). */
		overscroll-behavior: contain;
		background: var(--bg);
		border-top: 1px solid var(--border);
		border-bottom: 1px solid var(--border);
		font-size: var(--fs-small);
		cursor: default;
	}
	.view:focus {
		outline: none;
	}
	.left,
	.right {
		min-width: 0;
		min-height: 0;
		overflow: auto;
		overscroll-behavior: contain;
		padding: var(--sp-3) var(--sp-4);
	}
	.right {
		padding: var(--sp-2) 0;
		border-left: 1px solid var(--border);
	}
	.note {
		grid-column: 1 / 3;
		margin: var(--sp-4);
		color: var(--text-2);
	}
	.problem p {
		color: var(--danger);
		margin: 0 0 var(--sp-3);
	}
	dl {
		display: grid;
		grid-template-columns: max-content minmax(0, 1fr);
		gap: 2px var(--sp-4);
		margin: 0;
	}
	dt {
		color: var(--text-3);
	}
	dd {
		margin: 0;
		min-width: 0;
		overflow-wrap: anywhere;
	}
	.t {
		unicode-bidi: isolate;
	}
	.mono {
		font-family: var(--font-mono);
		font-size: var(--fs-mono);
	}
	.link {
		display: block;
		padding: 0;
		border: 0;
		background: none;
		color: var(--accent);
		font: inherit;
		text-align: left;
		cursor: pointer;
		unicode-bidi: isolate;
	}
	.link:hover {
		text-decoration: underline;
	}
	.none {
		color: var(--text-3);
	}
	.message {
		margin: var(--sp-4) 0 0;
		font-family: var(--font-mono);
		font-size: var(--fs-mono);
		white-space: pre-wrap;
		overflow-wrap: anywhere;
		unicode-bidi: isolate;
		user-select: text;
	}
	.warn {
		margin: var(--sp-3) var(--sp-4);
		color: var(--warning);
	}
	.files {
		display: flex;
		flex-direction: column;
	}
	.dir,
	.file {
		display: flex;
		align-items: center;
		gap: var(--sp-3);
		min-width: 0;
		height: 22px;
		flex: none;
		padding-right: var(--sp-3);
		white-space: nowrap;
		overflow: hidden;
	}
	.dir {
		border: 0;
		background: none;
		color: var(--text);
		font: inherit;
		text-align: left;
		cursor: pointer;
	}
	.file {
		cursor: pointer;
	}
	.dir:hover,
	.file:hover {
		background: var(--bg-hover);
	}
	.name {
		padding: 0;
		border: 0;
		background: none;
		color: inherit;
		font: inherit;
		text-align: left;
		white-space: nowrap;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.dirname,
	.orig {
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		color: var(--text-3);
	}
	.counts {
		flex: none;
		margin-left: auto;
		font-family: var(--font-mono);
		font-size: var(--fs-mono);
		color: var(--text-2);
	}
	.counts.binary {
		font-family: inherit;
		font-style: italic;
	}
	.file.deleted .name {
		text-decoration: line-through;
		color: var(--text-2);
	}
	.strip {
		grid-column: 3;
		grid-row: 1;
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: var(--sp-2);
		padding-top: var(--sp-2);
		border-left: 1px solid var(--border);
	}
	.icon {
		display: inline-flex;
		align-items: center;
		border-color: transparent;
		background: transparent;
	}
</style>
