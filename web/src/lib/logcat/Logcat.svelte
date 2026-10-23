<script lang="ts">
	import { tick, untrack } from 'svelte';
	import Eraser from '@lucide/svelte/icons/eraser';
	import Pause from '@lucide/svelte/icons/pause';
	import Play from '@lucide/svelte/icons/play';
	import RotateCw from '@lucide/svelte/icons/rotate-cw';
	import type { Client, ConnectionState } from '../api/client';
	import { browserStorage } from '../ui/layout';
	import { LOG_ROW_PX } from '../ui/metrics';
	import { isMarker } from './entry';
	import { QueryBar } from './bar';
	import type { LogcatDevice } from './device';
	import type { MineSet } from './mine';
	import { lineParts, priorityClass } from './format';
	import { LogcatSession } from './session';
	import { rowWindow } from './virtual';

	// The panel is thin on purpose: the stream, the batching, the ring, the filters and the
	// follow / pause state are in the session (tested in Node); this component draws the
	// rows the scroll position shows and reports what the user did. Every device field is
	// rendered as a text node: there is no markup from data and no link (plan m2a D5).

	interface Props {
		client: Client;
		connection: ConnectionState;
		/** Which device to stream (D40): the toolbar's, or one picked here. */
		devices: LogcatDevice;
		/** This workspace's application ids, for `package:mine` (D39). */
		mine: MineSet;
		/** The remembered query is per project (D38). */
		workspaceId: string;
	}
	let { client, connection, devices, mine, workspaceId }: Props = $props();

	// Fixed row height, so the window is arithmetic (plan m2a step 5); LOG_ROW_PX is its one source.
	const OVERSCAN = 10;

	const session = new LogcatSession({
		client: {
			subscribe: (name, params, handlers) => client.subscribe(name, params, handlers),
			call: (name, params) => client.call(name, params),
		},
		hidden: document.hidden,
		onDeviceGone: (serial) => devices.deviceGone(serial),
	});
	const bar = new QueryBar({ session, mine: untrack(() => mine.ids), storage: browserStorage(window), workspaceId: untrack(() => workspaceId) });

	function snapshot() {
		return {
			count: session.count,
			newCount: session.newCount,
			following: session.following,
			paused: session.paused,
			held: session.held,
			capacity: session.capacity,
			drops: session.drops,
			status: session.status,
		};
	}

	let snap = $state.raw(snapshot());
	let barView = $state.raw(bar.view);
	let deviceView = $state.raw(untrack(() => devices.view));
	let mineCount = $state(untrack(() => mine.ids.size));
	let scroller = $state<HTMLDivElement>();
	let scrollTop = $state(0);
	let viewportHeight = $state(400);

	// The scroll position this component set itself, so its own scroll event is not
	// mistaken for the user leaving the bottom.
	let pinned = -1;

	const win = $derived(
		rowWindow({
			scrollTop,
			viewportHeight,
			rowHeight: LOG_ROW_PX,
			overscan: OVERSCAN,
			count: snap.count,
			following: snap.following,
		}),
	);
	const rows = $derived(session.rows(win.start, win.end));

	// Runs after every change the session reports: once per animation frame while lines
	// arrive. One state assignment, one render, one scroll.
	async function changed() {
		const shift = session.takeShift();
		// Rows the ring forgot from the front: keep the lines the user is reading in place.
		if (!session.following && shift > 0) scrollTop = Math.max(0, scrollTop - shift * LOG_ROW_PX);
		snap = snapshot();
		await tick();
		if (!scroller) return;
		if (snap.following) {
			pinned = Math.max(0, snap.count * LOG_ROW_PX - viewportHeight);
			scroller.scrollTop = pinned;
		} else if (shift > 0) {
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
			if (snap.following) {
				pinned = Math.max(0, snap.count * LOG_ROW_PX - el.clientHeight);
				el.scrollTop = pinned;
			} else {
				el.scrollTop = scrollTop;
			}
		});
	});
	$effect(() => {
		const off = session.onChange(changed);
		return () => {
			off();
			session.dispose();
		};
	});
	$effect(() => {
		const d = devices;
		const m = mine;
		const offDevice = d.subscribe(() => (deviceView = d.view));
		// An id joining the set changes what `package:mine` shows: the held lines are tested again.
		const offMine = m.subscribe(() => {
			mineCount = m.ids.size;
			bar.reapply();
			barView = bar.view;
		});
		deviceView = d.view;
		mineCount = m.ids.size;
		return () => {
			offDevice();
			offMine();
		};
	});
	// The session is an object outside Svelte's state: these effects tell it what the props
	// and the device now are. Its callbacks assign state, so the calls are untracked: the
	// effects depend on the arguments alone.
	$effect(() => {
		const state = connection;
		untrack(() => session.onConnection(state));
	});
	$effect(() => {
		const serial = deviceView.serial;
		untrack(() => session.select(serial));
	});

	function onscroll() {
		if (!scroller) return;
		const top = scroller.scrollTop;
		scrollTop = top;
		if (Math.abs(top - pinned) < 1) return; // our own scroll to the bottom
		session.setFollowing(scroller.scrollHeight - top - scroller.clientHeight <= LOG_ROW_PX * 2);
	}

	function oninput(e: Event & { currentTarget: HTMLInputElement }) {
		bar.set(e.currentTarget.value);
		barView = bar.view;
	}

	function toBottom() {
		if (session.paused) session.setPaused(false);
		else session.setFollowing(true);
	}

	function statusText(s: typeof snap.status): string {
		return s.detail === '' ? s.state : `${s.state}: ${s.detail}`;
	}

	const canRestart = $derived(snap.status.state === 'stopped' || snap.status.state === 'error');
	const idleText = $derived(deviceView.pendingAvd !== '' ? 'AVD not running' : 'No device selected.');
</script>

<svelte:document onvisibilitychange={() => session.setHidden(document.hidden)} />

<section aria-label="Logcat">
	<h2>Logcat</h2>

	<div class="bar">
		<select
			aria-label="Logcat device"
			title="Which device's logcat to show"
			value={deviceView.pick}
			onchange={(e) => devices.select(e.currentTarget.value)}
		>
			<option value="">Toolbar device</option>
			{#each deviceView.options as d (d.device)}
				<option value={d.device}>{d.avdName ? `${d.avdName} (${d.device})` : d.device}</option>
			{/each}
		</select>
		<input
			type="text"
			class="query"
			aria-label="Logcat query"
			aria-invalid={barView.error !== null}
			aria-describedby={barView.error !== null ? 'logcat-query-error' : undefined}
			placeholder="package:mine level:warn"
			spellcheck="false"
			autocomplete="off"
			value={barView.text}
			{oninput}
		/>
		<button type="button" class="icon" title={snap.paused ? 'Resume' : 'Pause'} aria-label={snap.paused ? 'Resume' : 'Pause'} onclick={() => session.setPaused(!snap.paused)}>
			{#if snap.paused}<Play size={14} />{:else}<Pause size={14} />{/if}
		</button>
		<button type="button" class="icon" title="Clear" aria-label="Clear" onclick={() => session.clear()}><Eraser size={14} /></button>
		<button type="button" class="icon" title="Restart the stream" aria-label="Restart the stream" disabled={!canRestart} onclick={() => session.restart()}>
			<RotateCw size={14} />
		</button>
	</div>
	{#if barView.error}
		<p class="error" id="logcat-query-error" role="alert">{barView.error.message} (at column {barView.error.at + 1})</p>
	{:else if barView.usesMine && mineCount === 0}
		<p class="hint" id="logcat-mine-empty">no application id known yet: build or install once</p>
	{/if}

	<div class="frame">
		<div
			class="viewport"
			role="log"
			aria-label="Log lines"
			bind:this={scroller}
			bind:clientHeight={viewportHeight}
			{onscroll}
		>
			<div class="spacer" style:height="{win.totalHeight}px" style:--log-row="{LOG_ROW_PX}px">
				<div class="rows" id="logcat-rows" style:transform="translateY({win.offset}px)">
					{#each rows as row (row.seq)}
						{#if isMarker(row.entry)}
							<div class="row marker">{row.entry.text}</div>
						{:else}
							{@const p = lineParts(row.entry)}
							<div class={['row', priorityClass(row.entry.priority)]}><span class="ts">{p.time}</span> <span class="pid">{p.pid}</span> {p.rest} <span class="lv">{p.level}</span> {p.text}</div>
						{/if}
					{/each}
				</div>
			</div>
		</div>
		{#if snap.newCount > 0}
			<button type="button" class="pill" onclick={toBottom}>
				{snap.newCount} new {snap.newCount === 1 ? 'line' : 'lines'}
			</button>
		{/if}
	</div>

	<footer>
		<span class="status" id="logcat-status">{deviceView.serial === '' ? idleText : `Stream: ${statusText(snap.status)}`}</span>
		·
		Held <span id="logcat-held">{snap.held}</span> of {snap.capacity} · dropped:
		daemon <span id="logcat-dropped-daemon">{snap.drops.daemon}</span>, hidden tab
		<span id="logcat-dropped-queue">{snap.drops.queue}</span>, over the cap
		<span id="logcat-dropped-ring">{snap.drops.ring}</span>
	</footer>
</section>

<style>
	section {
		flex: 1;
		min-height: 0;
		display: flex;
		flex-direction: column;
	}
	.bar {
		flex: none;
		display: flex;
		gap: var(--sp-4);
		align-items: center;
		padding: var(--sp-4) var(--sp-6);
		border-bottom: 1px solid var(--border);
	}
	.query {
		flex: 1;
		min-width: 0;
		font-family: var(--font-mono);
		font-size: var(--fs-mono);
	}
	.query[aria-invalid='true'] {
		border-color: var(--danger);
	}
	.icon {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		padding: var(--sp-2);
	}
	.error,
	.hint {
		flex: none;
		margin: var(--sp-2) 0 0;
		font-size: var(--fs-small);
	}
	.error {
		color: var(--danger);
	}
	.hint {
		color: var(--text-2);
	}
	.frame {
		position: relative;
		flex: 1;
		/* Three rows and the viewport's border. */
		min-height: 56px;
		display: flex;
		flex-direction: column;
	}
	.viewport {
		flex: 1;
		min-height: 0;
		overflow-y: auto;
		overscroll-behavior: contain;
		background: var(--bg-window);
	}
	.spacer {
		position: relative;
		overflow: hidden;
	}
	.rows {
		font-family: var(--font-mono);
		font-size: var(--fs-log);
		line-height: var(--log-row);
		padding: 0 var(--sp-6);
	}
	.ts {
		color: var(--text-2);
	}
	.pid {
		color: var(--text-3);
	}
	.lv {
		display: inline-block;
		width: 16px;
		text-align: center;
		font-weight: 500;
	}
	.row {
		height: var(--log-row);
		white-space: pre;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.p-warn {
		color: var(--warning);
	}
	.p-error,
	.p-fatal {
		color: var(--danger);
		background: var(--danger-soft);
	}
	.p-verbose {
		color: var(--text-3);
	}
	.p-debug {
		color: var(--text-2);
	}
	.marker {
		font-style: italic;
		background: var(--bg-raised);
		color: var(--text-2);
	}
	.pill {
		position: absolute;
		right: 1.5rem;
		bottom: 0.75rem;
	}
	footer {
		flex: none;
		margin: var(--sp-2) 0 0;
	}
</style>
