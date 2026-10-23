<script lang="ts">
	import { tick, untrack } from 'svelte';
	import { rowWindow } from '../logcat/virtual';
	import { failureText } from './failure';
	import type { ConsoleModel, ConsoleView } from './console';

	// The console of the newest build (plan m2c step 9): the model keeps the lines and the
	// stream; this component draws the rows the scroll position shows, on 2a's fixed-height
	// virtual list, and reports what the user did. Every line is a text node, never markup.
	interface Props {
		model: ConsoleModel;
	}
	let { model }: Props = $props();

	/** Fixed row height in px, so the window is arithmetic (as Logcat's). */
	const ROW_HEIGHT = 18;
	const OVERSCAN = 10;

	let view = $state.raw<ConsoleView | null>(null);
	let scroller = $state<HTMLDivElement>();
	let scrollTop = $state(0);
	let viewportHeight = $state(400);
	let following = $state(true);

	// The scroll position this component set itself, so its own scroll event is not
	// mistaken for the user leaving the bottom.
	let pinned = -1;
	let shownBuild: string | null = null;
	let startSeq = 0;

	const win = $derived(
		rowWindow({
			scrollTop,
			viewportHeight,
			rowHeight: ROW_HEIGHT,
			overscan: OVERSCAN,
			count: view?.count ?? 0,
			following,
		}),
	);
	const rows = $derived(view ? model.rows(win.start, win.end) : []);

	// Runs after every change the model reports: once per animation frame while lines arrive.
	async function read(m: ConsoleModel) {
		const v = m.view;
		if (v.buildId !== shownBuild) {
			// Another build: start at its bottom.
			shownBuild = v.buildId;
			startSeq = v.startSeq;
			following = true;
		}
		// Lines forgotten from the front, or earlier ones added before it: keep the line being read in place.
		const shift = v.startSeq - startSeq;
		startSeq = v.startSeq;
		if (!following && shift !== 0) scrollTop = Math.max(0, scrollTop - shift * ROW_HEIGHT);
		view = v;
		await tick();
		if (!scroller) return;
		if (following) {
			pinned = Math.max(0, v.count * ROW_HEIGHT - viewportHeight);
			scroller.scrollTop = pinned;
		} else if (shift !== 0) {
			scroller.scrollTop = scrollTop;
		}
	}

	// The viewport's height changed (a bottom-region resize, a tab shown again): following, the
	// last line is brought back into view; not following, the scroller gets its place back.
	$effect(() => {
		void viewportHeight;
		const el = scroller;
		untrack(() => {
			if (!el) return;
			if (following) {
				pinned = Math.max(0, (view?.count ?? 0) * ROW_HEIGHT - el.clientHeight);
				el.scrollTop = pinned;
			} else {
				el.scrollTop = scrollTop;
			}
		});
	});

	$effect(() => {
		const m = model;
		untrack(() => void read(m));
		return m.subscribe(() => void read(m));
	});

	function onscroll() {
		if (!scroller) return;
		const top = scroller.scrollTop;
		scrollTop = top;
		if (Math.abs(top - pinned) < 1) return; // our own scroll to the bottom
		following = scroller.scrollHeight - top - scroller.clientHeight <= ROW_HEIGHT * 2;
	}

	function watchText(w: ConsoleView['watch']): string {
		return w.detail === '' ? w.state : `${w.state}: ${w.detail}`;
	}
</script>

<section aria-label="Build console">
	<h2>Console</h2>
	{#if !view}
		<p>Loading...</p>
	{:else}
		<p id="console-status">
			Output watch: {watchText(view.watch)}.
			{#if view.hasBuild}
				Build {view.buildId}{view.live ? ' is running' : ''}.
				{#if view.offset !== null}
					From byte {view.offset} of {view.size ?? 'unknown'}{view.growing ? ', still growing' : ''}.
				{/if}
			{:else}
				No build console yet.
			{/if}
			{#if view.hasEarlier}
				<button type="button" disabled={view.loading} onclick={() => void model.earlier()}>Earlier</button>
			{/if}
			{#if !following && view.hasBuild}
				<button
					type="button"
					onclick={() => {
						following = true;
						void read(model);
					}}
				>
					To the bottom
				</button>
			{/if}
			{#if view.loading}
				Loading...
			{/if}
		</p>
		{#if view.error}
			<p>{failureText(view.error)}</p>
		{/if}
		{#if view.hasBuild}
			<div
				class="viewport"
				role="log"
				aria-label="Console lines"
				bind:this={scroller}
				bind:clientHeight={viewportHeight}
				{onscroll}
			>
				<div class="spacer" style:height="{win.totalHeight}px">
					<div class="rows" id="console-rows" style:transform="translateY({win.offset}px)">
						{#each rows as row (row.seq)}
							<div class="row">{row.text}</div>
						{/each}
					</div>
				</div>
			</div>
			<footer>
				Held <span id="console-held">{view.count}</span> lines, forgotten over the cap
				<span id="console-evicted">{view.evicted}</span>.
			</footer>
		{/if}
	{/if}
</section>

<style>
	section {
		flex: 1;
		min-height: 0;
		display: flex;
		flex-direction: column;
	}
	section > p {
		flex: none;
	}
	.viewport {
		flex: 1;
		/* Three rows and the border. */
		min-height: 56px;
		overflow-y: auto;
		overscroll-behavior: contain;
		border: 1px solid var(--border);
	}
	.spacer {
		position: relative;
		overflow: hidden;
	}
	.rows {
		font: 12px/18px ui-monospace, monospace;
	}
	.row {
		height: 18px;
		white-space: pre;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	footer {
		flex: none;
		margin-top: 0.5rem;
	}
</style>
