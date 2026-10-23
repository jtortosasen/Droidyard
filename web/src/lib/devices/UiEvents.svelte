<script lang="ts">
	import { untrack } from 'svelte';
	import Eraser from '@lucide/svelte/icons/eraser';
	import RefreshCw from '@lucide/svelte/icons/refresh-cw';
	import type { ConnectionState } from '../api/client';
	import type { GapRow, StopReason, UiEventsModel, UiEventsView } from './uievents';
	import { shortClass } from './uitree';
	import { inert, quoted } from './text';

	// The screen's events of the toolbar's device, live (plan m2w step 7; ADR-0112): the model
	// decides what is watched, this draws. Newest first. Every field the device sent (package,
	// class, a toast's text) is a text node, never markup, never a link, never a class or style.
	interface Props {
		model: UiEventsModel;
		connection: ConnectionState;
		/** The panel's region is open. */
		visible: boolean;
	}
	let { model, connection, visible }: Props = $props();

	let view = $state.raw<UiEventsView>(untrack(() => model.view));
	let pageHidden = $state(document.hidden);

	$effect(() => {
		const m = model;
		const read = () => {
			view = m.view;
		};
		read();
		return m.subscribe(read);
	});

	// Watching costs the daemon a request every 250 ms: only while the panel can be seen.
	$effect(() => model.setActive(visible && !pageHidden));
	$effect(() => model.onConnection(connection));

	const WHY: Record<StopReason, string> = {
		deviceGone: 'The device went away.',
		replaced: 'Another stream of this device took this one over.',
		failed: 'The events could not be read.',
	};

	function gapText(g: GapRow): string {
		switch (g.reason) {
			case 'readerStarted':
				return 'The reader started here: earlier events were not recorded.';
			case 'readerRestarted':
				return 'The reader restarted: events before this point may be missing.';
			case 'overflow':
				return g.dropped > 0
					? `${g.dropped} events were lost: the reader's ring moved past this panel.`
					: "Events were lost: the reader's ring moved past this panel.";
			case 'interrupted':
				return 'Not watching for a while: events during this time are missing.';
		}
	}
</script>

<svelte:document onvisibilitychange={() => (pageHidden = document.hidden)} />

<section class="events">
	<div class="head">
		<h2>UI events</h2>
		<button type="button" class="btn-icon" disabled={view.rows.length === 0} onclick={() => model.clear()}>
			<Eraser size={12} /> Clear
		</button>
	</div>
	{#if view.state === 'idle'}
		<p class="muted">Select a device first.</p>
	{:else if view.state === 'stopped'}
		<p role="status">
			{view.reason ? WHY[view.reason] : ''}
			{inert(view.cause)}
			<button type="button" class="btn-icon" onclick={() => model.restart()}>
				<RefreshCw size={12} /> Restart
			</button>
		</p>
	{:else if view.rows.length === 0}
		<p class="muted">Waiting for events from {inert(view.device)}...</p>
	{/if}
	{#if view.rows.length > 0}
		<ol class="list" aria-label="UI events, newest first">
			{#each view.rows as row (row.id)}
				{#if row.kind === 'gap'}
					<li class="gap muted small">{gapText(row)}</li>
				{:else}
					<li class="row">
						<span class="clock">{row.clock}</span>
						<span class="type">{row.type}</span>
						{#if row.count > 1}<span class="muted">x{row.count}</span>{/if}
						{#if row.class !== ''}<span class="cls">{shortClass(inert(row.class))}</span>{/if}
						{#if row.package !== ''}<span class="muted pkg">{inert(row.package)}</span>{/if}
						{#if row.window !== ''}<span class="muted">window {row.window}</span>{/if}
						{#if row.text !== ''}<span class="txt">{quoted(row.text)}</span>{/if}
					</li>
				{/if}
			{/each}
		</ol>
	{/if}
</section>

<style>
	.head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--sp-3);
	}
	.head :global(h2) {
		margin: 0;
	}
	.list {
		margin: var(--sp-3) 0 0;
		max-height: 260px;
		overflow: auto;
		padding: var(--sp-1);
		list-style: none;
		border: 1px solid var(--border);
		border-radius: var(--radius-control);
		background: var(--bg-window);
	}
	.row {
		display: flex;
		align-items: baseline;
		gap: var(--sp-3);
		min-height: var(--row);
		padding: 0 var(--sp-3);
		font-family: var(--font-mono);
		font-size: var(--fs-mono);
		overflow: hidden;
		white-space: nowrap;
	}
	.gap {
		padding: var(--sp-1) var(--sp-3);
		border-top: 1px dashed var(--border);
		border-bottom: 1px dashed var(--border);
	}
	.clock {
		flex: none;
		color: var(--text-3);
	}
	.type {
		flex: none;
		color: var(--accent);
	}
	.cls,
	.pkg,
	.txt {
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
	}
</style>
