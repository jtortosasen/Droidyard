<script lang="ts">
	import { tick, untrack } from 'svelte';
	import type { Client, ConnectionState } from '../api/client';
	import { isMarker, PRIORITIES, type Priority } from './entry';
	import { parseQuery } from './query';
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
		/** The serial to stream; '' for none. */
		device: string;
	}
	let { client, connection, device }: Props = $props();

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
	let scroller = $state<HTMLDivElement>();
	let scrollTop = $state(0);
	let viewportHeight = $state(400);

	let minPriority = $state<Priority>('verbose');
	let tagsText = $state('');
	let text = $state('');
	let applicationText = $state('');
	let applicationId = $state('');

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
	// The session is an object outside Svelte's state: these effects tell it what the props
	// and the application field now are. Its callbacks assign state, so the calls are
	// untracked: the effects depend on the arguments alone.
	$effect(() => {
		const state = connection;
		untrack(() => session.onConnection(state));
	});
	$effect(() => {
		const serial = device;
		const app = applicationId;
		untrack(() => session.select(serial, app));
	});

	function onscroll() {
		if (!scroller) return;
		const top = scroller.scrollTop;
		scrollTop = top;
		if (Math.abs(top - pinned) < 1) return; // our own scroll to the bottom
		session.setFollowing(scroller.scrollHeight - top - scroller.clientHeight <= ROW_HEIGHT * 2);
	}

	function applyFilter() {
		const r = parseQuery(legacyQuery(minPriority, tagsText, text));
		if (r.ok) session.setFilter(r.predicate);
	}

	// Step 3 stopgap: the three old controls, written as a query. Step 4 replaces them
	// with the query bar. Tags are now exact but case-insensitive.
	function legacyQuery(min: Priority, tags: string, message: string): string {
		const q = (s: string) => `"${s.replace(/[\\"]/g, '\\$&')}"`;
		const parts: string[] = [];
		if (min !== 'verbose') parts.push(`level:${min}`);
		for (const t of tags.split(/[\s,]+/).filter((t) => t !== '')) parts.push(`tag=:${q(t)}`);
		if (message !== '') parts.push(`message:${q(message)}`);
		return parts.join(' ');
	}

	function applyApplication(e: SubmitEvent) {
		e.preventDefault();
		applicationId = applicationText.trim();
	}

	function toBottom() {
		if (session.paused) session.setPaused(false);
		else session.setFollowing(true);
	}

	function statusText(s: typeof snap.status): string {
		return s.detail === '' ? s.state : `${s.state}: ${s.detail}`;
	}
</script>

<svelte:document onvisibilitychange={() => session.setHidden(document.hidden)} />

<section aria-label="Logcat">
	<h2>Logcat</h2>

	<div class="controls">
		<label>
			Priority
			<select
				value={minPriority}
				onchange={(e) => {
					minPriority = e.currentTarget.value as Priority;
					applyFilter();
				}}
			>
				{#each PRIORITIES as p (p)}
					<option value={p}>{p}{p === 'verbose' ? '' : ' and above'}</option>
				{/each}
			</select>
		</label>
		<label>
			Tags
			<input
				type="text"
				value={tagsText}
				placeholder="exact tags, e.g. ActivityManager"
				oninput={(e) => {
					tagsText = e.currentTarget.value;
					applyFilter();
				}}
			/>
		</label>
		<label>
			Text
			<input
				type="text"
				value={text}
				placeholder="in the message"
				oninput={(e) => {
					text = e.currentTarget.value;
					applyFilter();
				}}
			/>
		</label>
		<form onsubmit={applyApplication}>
			<label>
				Application
				<input
					type="text"
					value={applicationText}
					placeholder="applicationId, e.g. dev.example.app"
					oninput={(e) => (applicationText = e.currentTarget.value)}
				/>
			</label>
			<button type="submit">Apply</button>
		</form>
		<button type="button" onclick={() => session.setPaused(!snap.paused)}>
			{snap.paused ? 'Resume' : 'Pause'}
		</button>
		<button type="button" onclick={() => session.clear()}>Clear</button>
		{#if snap.status.state === 'stopped' || snap.status.state === 'error'}
			<button type="button" onclick={() => session.restart()}>Restart stream</button>
		{/if}
	</div>

	<p class="status" id="logcat-status">
		{device === '' ? 'No device selected.' : `Stream: ${statusText(snap.status)}`}
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
	.controls {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem 1rem;
		align-items: center;
	}
	.controls form {
		display: flex;
		gap: 0.25rem;
		align-items: center;
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
