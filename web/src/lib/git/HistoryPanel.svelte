<script lang="ts">
	import { untrack } from 'svelte';
	import { COL, NODE_R, ROW, graphWidth, laneColor, laneX, strokes } from './graph';
	import { avatarHue, initials, relativeTime, type HistoryStore, type HistoryView } from './history';
	import { rowTop, rowWindow } from './rows';
	import type { GitWatch } from './watch';

	// The bottom Git panel's History tab (plan m2n step 4; ADR-0070 §1): the commits newest
	// first in a virtualised table, one inline <svg> per row for the graph. Columns: GRAPH &
	// SUBJECT, AUTHOR (an initials avatar coloured from the e-mail: the CSP admits no remote
	// image), SHA, COMMIT TIME (relative; the date in the title). The Uncommitted Changes (n)
	// row sits on top while the work tree differs from HEAD; HEAD's node is hollow; a merge is
	// dimmed. Every text from git (subject, name, e-mail, oid) is a text node in a cell with
	// `unicode-bidi: isolate`; nothing from git becomes markup. The store keeps the rows, the
	// pages and the reload rules; this component draws the window the scroll shows.
	interface Props {
		store: HistoryStore;
		/** Only for the reason when the status has not arrived (not a repository, no git). */
		watch: GitWatch;
	}
	let { store, watch }: Props = $props();

	const OVERSCAN = 8;
	/** Rows from the end of the loaded history at which the next page is read. */
	const NEAR_END = 100;

	let view = $state.raw<HistoryView>(untrack(() => store.view));
	let problem = $state<string | null>(null);
	$effect(() => {
		const s = store;
		view = s.view;
		return s.subscribe(() => {
			view = s.view;
		});
	});
	$effect(() => {
		const w = watch;
		problem = w.view.problem?.text ?? null;
		return w.subscribe(() => {
			problem = w.view.problem?.text ?? null;
		});
	});

	let now = $state(Date.now());
	$effect(() => {
		const id = setInterval(() => (now = Date.now()), 30_000);
		return () => clearInterval(id);
	});

	let scroller = $state<HTMLDivElement>();
	let scrollTop = $state(0);
	let viewportHeight = $state(0);

	const off = $derived(view.uncommitted > 0 ? 1 : 0);
	const count = $derived(view.rows.length + off);
	const win = $derived(rowWindow({ scrollTop, viewportHeight, rowHeight: ROW, overscan: OVERSCAN, count }));
	const indices = $derived(Array.from({ length: win.end - win.start }, (_, k) => win.start + k));
	const gw = $derived(graphWidth(view.width) + 6);
	const headRow = $derived(view.headOid === '' ? undefined : view.rows.find((r) => r.commit.oid === view.headOid));

	// The next page, when the window nears the end of what is loaded.
	$effect(() => {
		if (view.state === 'ready' && view.more && !view.loadingMore && !view.moreProblem && win.end >= count - NEAR_END) {
			untrack(() => store.loadMore());
		}
	});

	// A reload that finished says where the scroll goes: the oid at the top of the view, else the top.
	let restored = -1;
	$effect(() => {
		const r = view.restore;
		const el = scroller;
		if (!r || !el || r.seq === restored) return;
		restored = r.seq;
		const at = r.oid === null ? -1 : store.indexOf(r.oid);
		el.scrollTop = at < 0 ? 0 : rowTop(at + off, ROW);
	});

	function onscroll() {
		if (!scroller) return;
		scrollTop = scroller.scrollTop;
		// The commit at the top of the view; at the very top none, so a new commit shows.
		const i = Math.round(scrollTop / ROW) - off;
		store.setAnchor(scrollTop < ROW / 2 || i < 0 ? null : (view.rows[i]?.commit.oid ?? null));
	}

	function move(delta: number) {
		const rows = view.rows;
		if (rows.length === 0) return;
		const cur = view.selected === null ? -1 : store.indexOf(view.selected);
		const next = Math.min(rows.length - 1, Math.max(0, cur < 0 ? (delta > 0 ? 0 : rows.length - 1) : cur + delta));
		store.select(rows[next].commit.oid);
		const el = scroller;
		if (!el) return;
		const top = rowTop(next + off, ROW);
		if (top < el.scrollTop) el.scrollTop = top;
		else if (top + ROW > el.scrollTop + el.clientHeight) el.scrollTop = top + ROW - el.clientHeight;
	}

	function onkeydown(e: KeyboardEvent) {
		if (e.key === 'ArrowDown') move(1);
		else if (e.key === 'ArrowUp') move(-1);
		else return;
		e.preventDefault();
	}

	const when = (t: number) => new Date(t * 1000).toLocaleString();
	const uncommittedText = $derived(`Uncommitted Changes (${view.uncommitted}${view.uncommittedTruncated ? '+' : ''})`);
</script>

<section class="history" data-history>
	{#if view.state === 'idle'}
		<p class="note" role="status">{problem ?? 'Reading the status…'}</p>
	{:else if view.state === 'loading'}
		<p class="note" role="status">Reading the history…</p>
	{:else if view.state === 'error'}
		<div class="problem" role="alert">
			<p>{view.problem}</p>
			<button type="button" onclick={() => store.reload()}>Retry</button>
		</div>
	{:else if count === 0}
		<p class="note" role="status">No commits yet.</p>
	{:else}
		<div class="head" role="row">
			<span class="cell main" role="columnheader">GRAPH &amp; SUBJECT</span>
			<span class="cell" role="columnheader">AUTHOR</span>
			<span class="cell" role="columnheader">SHA</span>
			<span class="cell" role="columnheader">COMMIT TIME</span>
		</div>
		<div
			class="viewport"
			role="grid"
			aria-label="Commits"
			aria-rowcount={count}
			tabindex="0"
			bind:this={scroller}
			bind:clientHeight={viewportHeight}
			{onscroll}
			{onkeydown}
		>
			<div class="spacer" style:height="{win.totalHeight}px">
				<div class="rows" style:transform="translateY({win.offset}px)">
					{#each indices as i (i < off ? 'uncommitted' : view.rows[i - off].commit.oid)}
						{#if i < off}
							<div class="row uncommitted" role="row" aria-rowindex={i + 1} data-row="uncommitted">
								<div class="cell main" role="gridcell">
									<span class="graph" style:width="{gw}px">
										<svg width={graphWidth(view.width)} height={ROW} viewBox="0 0 {graphWidth(view.width)} {ROW}" aria-hidden="true">
											{#if headRow}
												<path d="M {laneX(headRow.lane.column)} {ROW / 2 + NODE_R} L {laneX(headRow.lane.column)} {ROW}" class="dash" stroke={laneColor(headRow.lane.color)} />
											{/if}
											<circle cx={laneX(headRow?.lane.column ?? 0)} cy={ROW / 2} r={NODE_R} class="dash node-hollow" stroke={headRow ? laneColor(headRow.lane.color) : 'var(--text-3)'} />
										</svg>
									</span>
									<span class="text subject" data-uncommitted={view.uncommitted}>{uncommittedText}</span>
								</div>
								<div class="cell"></div>
								<div class="cell"></div>
								<div class="cell"></div>
							</div>
						{:else}
							{@const r = view.rows[i - off]}
							{@const c = r.commit}
							{@const isHead = c.oid === view.headOid}
							<div
								class="row"
								class:merge={c.parents.length > 1}
								class:selected={view.selected === c.oid}
								role="row"
								tabindex="-1"
								aria-rowindex={i + 1}
								aria-selected={view.selected === c.oid}
								data-row="commit"
								data-oid={c.oid}
								onclick={() => store.select(c.oid)}
								onkeydown={(e) => {
									if (e.key === 'Enter') store.select(c.oid);
								}}
							>
								<div class="cell main" role="gridcell">
									<span class="graph" style:width="{gw}px">
										<svg width={graphWidth(view.width)} height={ROW} viewBox="0 0 {graphWidth(view.width)} {ROW}" aria-hidden="true">
											{#each strokes(r.lane) as s, k (k)}
												<path d={s.d} class="lane" stroke={laneColor(s.color)} />
											{/each}
											{#if isHead}
												<circle cx={laneX(r.lane.column)} cy={ROW / 2} r={NODE_R - 0.5} class="node-hollow" stroke={laneColor(r.lane.color)} data-head />
											{:else}
												<circle cx={laneX(r.lane.column)} cy={ROW / 2} r={NODE_R} fill={laneColor(r.lane.color)} class="node" />
											{/if}
										</svg>
									</span>
									<span class="text subject" title={c.subject}>{c.subject}</span>
								</div>
								<div class="cell" role="gridcell">
									<span class="avatar" style:--hue={avatarHue(c.authorEmail)} aria-hidden="true">{initials(c.authorName, c.authorEmail)}</span>
									<span class="text" title="{c.authorName} <{c.authorEmail}>">{c.authorName}</span>
								</div>
								<div class="cell" role="gridcell"><span class="text sha" title={c.oid}>{c.oid.slice(0, 7)}</span></div>
								<div class="cell" role="gridcell"><span class="text" title={when(c.commitTime)}>{relativeTime(c.commitTime, now)}</span></div>
							</div>
						{/if}
					{/each}
				</div>
			</div>
		</div>
		{#if view.loadingMore || view.moreProblem}
			<footer>
				{#if view.moreProblem}
					<span class="error" role="alert">{view.moreProblem}</span>
					<button type="button" onclick={() => store.loadMore()}>Retry</button>
				{:else}
					<span role="status">Reading more…</span>
				{/if}
			</footer>
		{/if}
	{/if}
</section>

<style>
	.history {
		flex: 1;
		min-height: 0;
		display: flex;
		flex-direction: column;
		font-size: var(--fs-base);
	}
	.note,
	.problem {
		margin: var(--sp-4);
		color: var(--text-2);
	}
	.problem p {
		color: var(--danger);
		margin: 0 0 var(--sp-3);
	}
	.head,
	.row {
		display: grid;
		grid-template-columns: minmax(0, 1fr) 160px 70px 110px;
		align-items: center;
	}
	.head {
		flex: none;
		height: var(--row);
		font-size: var(--fs-small);
		color: var(--text-3);
		letter-spacing: 0.04em;
		border-bottom: 1px solid var(--border);
		scrollbar-gutter: stable;
		overflow: hidden;
		padding-right: 15px;
	}
	.viewport {
		flex: 1;
		min-height: 0;
		overflow-y: scroll;
		overscroll-behavior: contain;
		background: var(--bg-panel);
	}
	.spacer {
		position: relative;
		overflow: hidden;
	}
	.row {
		height: 24px;
		cursor: default;
	}
	.row:hover {
		background: var(--bg-hover);
	}
	.row.selected {
		background: var(--accent-soft);
	}
	.cell {
		display: flex;
		align-items: center;
		gap: var(--sp-3);
		min-width: 0;
		padding: 0 var(--sp-3);
		overflow: hidden;
		white-space: nowrap;
	}
	.cell.main {
		padding-left: 0;
	}
	.text {
		unicode-bidi: isolate;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.sha {
		font-family: var(--font-mono);
		font-size: var(--fs-mono);
		color: var(--text-2);
	}
	.cell:nth-child(n + 3) .text {
		color: var(--text-2);
	}
	.merge .text,
	.merge .node {
		opacity: 0.55;
	}
	.graph {
		flex: none;
		max-width: 45%;
		overflow: hidden;
		display: block;
		height: 24px;
	}
	svg {
		display: block;
	}
	.lane {
		fill: none;
		stroke-width: 2;
	}
	.node-hollow {
		fill: var(--bg-panel);
		stroke-width: 2;
	}
	.dash {
		fill: none;
		stroke-width: 1.5;
		stroke-dasharray: 3 2;
	}
	.uncommitted .subject {
		font-style: italic;
		color: var(--text-2);
	}
	.avatar {
		flex: none;
		width: 16px;
		height: 16px;
		border-radius: 50%;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		font-size: 8px;
		font-weight: 600;
		color: #fff;
		background: hsl(var(--hue) 45% 42%);
		user-select: none;
	}
	footer {
		flex: none;
		padding: var(--sp-2) var(--sp-4);
		font-size: var(--fs-small);
		color: var(--text-2);
		display: flex;
		gap: var(--sp-4);
		align-items: center;
	}
	.error {
		color: var(--danger);
	}
</style>
