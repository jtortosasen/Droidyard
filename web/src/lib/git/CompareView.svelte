<script lang="ts">
	import X from '@lucide/svelte/icons/x';
	import { aheadTitle, behindTitle, type CompareDirection, type CompareList, type CompareModel, type CompareView } from './compare';
	import { compareFileOf, type CommitFile } from './diffsource';
	import { listRows } from './files';
	import { relativeTime } from './history';

	// Compare with current (plan m2n P9; ADR-0072), in the bottom Git tab in place of the history table: the
	// commits the branch has that the current one lacks, the reverse, and the files between the two tips, a
	// file opening in the Diff tab with HEAD's text against the tip's, both read-only. Every text from git
	// (branch, subject, author, path) is a text node.
	interface Props {
		model: CompareModel;
		view: CompareView;
		/** A file between the tips was picked: the shell opens the Diff tab on it. */
		onopenfile: (file: CommitFile) => void;
		now: number;
	}
	let { model, view, onopenfile, now }: Props = $props();

	const files = $derived(view.files.show === null ? [] : listRows(view.files.show.files));
	const lists = $derived<{ id: CompareDirection; title: string; list: CompareList }[]>([
		{ id: 'ahead', title: aheadTitle(view.branch, view.current), list: view.ahead },
		{ id: 'behind', title: behindTitle(view.branch, view.current), list: view.behind },
	]);
</script>

<section class="compare" data-compare-view data-branch={view.branch} aria-label="Compare {view.branch} with {view.current}">
	<header>
		<h3 class="t">{view.branch} compared with {view.current}</h3>
		<button type="button" class="icon" title="Close the comparison" aria-label="Close the comparison" data-compare-close onclick={() => model.close()}><X size={14} /></button>
	</header>
	<div class="cols">
		{#each lists as l (l.id)}
			<div class="col" data-compare-list={l.id}>
				<h4 class="t" data-compare-title>{l.title}</h4>
				{#if l.list.state === 'loading'}
					<p class="note" role="status">Reading the commits…</p>
				{:else if l.list.state === 'error'}
					<div class="note" role="alert">
						<p>{l.list.problem}</p>
						<button type="button" onclick={() => model.retry(l.id)}>Retry</button>
					</div>
				{:else if l.list.commits.length === 0}
					<p class="note" data-compare-empty>No commits.</p>
				{:else}
					<ul>
						{#each l.list.commits as c (c.oid)}
							<li data-oid={c.oid}>
								<span class="subject t" title={c.subject}>{c.subject}</span>
								<span class="author t" title="{c.authorName} <{c.authorEmail}>">{c.authorName}</span>
								<span class="sha" title={c.oid}>{c.oid.slice(0, 7)}</span>
								<span class="when">{relativeTime(c.commitTime, now)}</span>
							</li>
						{/each}
					</ul>
					{#if l.list.problem}<p class="note" role="alert">{l.list.problem}</p>{/if}
					{#if l.list.more}
						<button type="button" disabled={l.list.loadingMore} data-compare-more={l.id} onclick={() => model.loadMore(l.id)}>
							{l.list.loadingMore ? 'Reading more…' : 'Load more'}
						</button>
					{/if}
				{/if}
			</div>
		{/each}
		<div class="col" data-compare-files>
			<h4 class="t">Files that differ</h4>
			{#if view.files.state === 'loading'}
				<p class="note" role="status">Reading the files…</p>
			{:else if view.files.state === 'error'}
				<div class="note" role="alert">
					<p>{view.files.problem}</p>
					<button type="button" onclick={() => model.retryFiles()}>Retry</button>
				</div>
			{:else if files.length === 0}
				<p class="note" data-compare-empty>No file differs.</p>
			{:else}
				<ul>
					{#each files as f (f.key)}
						<li
							class="file {f.status}"
							data-compare-file={f.path}
							data-status={f.status}
							data-added={f.added}
							data-removed={f.removed}
							data-binary={f.binary}
						>
							<button type="button" class="name t" title="Open in the Diff tab: {f.path}" onclick={() => onopenfile(compareFileOf(view.tip, view.head, f))}>{f.label}</button>
							{#if f.dir !== ''}<span class="dirname t">{f.dir}</span>{/if}
							{#if f.origPath !== undefined}<span class="dirname t">from {f.origPath}</span>{/if}
							{#if f.counts !== ''}<span class="counts">{f.counts}</span>{/if}
						</li>
					{/each}
				</ul>
				{#if view.files.show?.truncated}
					<p class="note" data-files-truncated>More files differ than are listed.</p>
				{/if}
			{/if}
		</div>
	</div>
</section>

<style>
	.compare {
		flex: 1;
		min-width: 0;
		min-height: 0;
		display: flex;
		flex-direction: column;
		font-size: var(--fs-small);
	}
	header {
		flex: none;
		display: flex;
		align-items: center;
		gap: var(--sp-3);
		padding: var(--sp-2) var(--sp-3);
		border-bottom: 1px solid var(--border);
	}
	h3,
	h4 {
		margin: 0;
		font-size: var(--fs-small);
		font-weight: 600;
	}
	header h3 {
		flex: 1;
	}
	.t {
		unicode-bidi: isolate;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.cols {
		flex: 1;
		min-height: 0;
		display: grid;
		grid-template-columns: repeat(3, minmax(0, 1fr));
	}
	.col {
		min-width: 0;
		min-height: 0;
		overflow: auto;
		padding: var(--sp-2) var(--sp-3);
		border-right: 1px solid var(--border);
	}
	ul {
		list-style: none;
		margin: var(--sp-2) 0 0;
		padding: 0;
	}
	li {
		display: flex;
		align-items: center;
		gap: var(--sp-3);
		padding: 2px 0;
	}
	.subject,
	.name {
		flex: 1;
		text-align: left;
	}
	.name {
		flex: 0 1 auto;
		border-color: transparent;
		background: transparent;
	}
	.author,
	.dirname {
		color: var(--text-2);
	}
	.sha,
	.when,
	.counts {
		flex: none;
		color: var(--text-3);
	}
	.note {
		margin: var(--sp-2) 0;
		color: var(--text-2);
	}
	.icon {
		border-color: transparent;
		background: transparent;
	}
</style>
