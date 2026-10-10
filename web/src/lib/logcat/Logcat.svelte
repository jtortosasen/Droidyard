<script lang="ts">
	import { tick, untrack } from 'svelte';
	import Eraser from '@lucide/svelte/icons/eraser';
	import Pause from '@lucide/svelte/icons/pause';
	import Play from '@lucide/svelte/icons/play';
	import RotateCw from '@lucide/svelte/icons/rotate-cw';
	import type { Client, ConnectionState } from '../api/client';
	import { browserStorage } from '../ui/layout';
	import { isMarker } from './entry';
	import { QueryBar } from './bar';
	import type { LogcatDevice } from './device';
	import type { MineSet } from './mine';
	import { formatLine, priorityClass } from './format';
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

	/** Fixed row height in px, so the window is arithmetic (plan m2a step 5). */
	const ROW_HEIGHT = 18;
	const OVERSCAN = 10;

	const session = new LogcatSession({
		client: {
			subscribe: (name, params, handlers) => client.subscribe(name, params, handlers),
			call: (name, params) => client.call(name, params),
		},
		hidden: document.hidden,
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
			rowHeight: ROW_HEIGHT,
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
		if (!session.following && shift > 0) scrollTop = Math.max(0, scrollTop - shift * ROW_HEIGHT);
		snap = snapshot();
		await tick();
		if (!scroller) return;
		if (snap.following) {
			pinned = Math.max(0, snap.count * ROW_HEIGHT - viewportHeight);
			scroller.scrollTop = pinned;
		} else if (shift > 0) {
			scroller.scrollTop = scrollTop;
		}
	}

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
		session.setFollowing(scroller.scrollHeight - top - scroller.clientHeight <= ROW_HEIGHT * 2);
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

	<p class="status" id="logcat-status">
		{deviceView.serial === '' ? idleText : `Stream: ${statusText(snap.status)}`}
	</p>

	<div class="frame">
		<div
			class="viewport"
			role="log"
			aria-label="Log lines"
			bind:this={scroller}
			bind:clientHeight={viewportHeight}
			{onscroll}
		>
			<div class="spacer" style:height="{win.totalHeight}px">
				<div class="rows" id="logcat-rows" style:transform="translateY({win.offset}px)">
					{#each rows as row (row.seq)}
						{#if isMarker(row.entry)}
							<div class="row marker">{row.entry.text}</div>
						{:else}
							<div class={['row', priorityClass(row.entry.priority)]}>{formatLine(row.entry)}</div>
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
		Held <span id="logcat-held">{snap.held}</span> of {snap.capacity} · dropped:
		daemon <span id="logcat-dropped-daemon">{snap.drops.daemon}</span>, hidden tab
		<span id="logcat-dropped-queue">{snap.drops.queue}</span>, over the cap
		<span id="logcat-dropped-ring">{snap.drops.ring}</span>
	</footer>
</section>

<style>
	.bar {
		display: flex;
		gap: var(--sp-2);
		align-items: center;
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
		margin: var(--sp-2) 0 0;
		font-size: var(--fs-small);
	}
	.error {
		color: var(--danger);
	}
	.hint {
		color: var(--text-2);
	}
	.status {
		margin: 0.5rem 0;
	}
	.frame {
		position: relative;
	}
	.viewport {
		height: 60vh;
		overflow-y: auto;
		border: 1px solid var(--border);
		border-radius: var(--radius-control);
		background: var(--bg-window);
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
	.p-warn {
		color: #8a5a00;
	}
	.p-error,
	.p-fatal {
		color: #b00020;
	}
	.p-verbose,
	.p-debug {
		color: #555;
	}
	.marker {
		font-style: italic;
		background: #eee;
		color: #333;
	}
	.pill {
		position: absolute;
		right: 1.5rem;
		bottom: 0.75rem;
	}
	footer {
		margin-top: 0.5rem;
	}
</style>
