<script lang="ts">
	import { untrack } from 'svelte';
	import Circle from '@lucide/svelte/icons/circle';
	import Square from '@lucide/svelte/icons/square';
	import Download from '@lucide/svelte/icons/download';
	import Trash from '@lucide/svelte/icons/trash';
	import type { ConnectionState } from '../api/client';
	import { canDownload, clockText, elapsedSeconds, type UiRecording, type UiRecordingModel, type UiRecordingView } from './uirecording';
	import { inert } from './text';

	// UI recordings (plan m2x step 8; ADR-0113 §6): Record or Stop for the toolbar's device, and the
	// recordings the daemon keeps. The model decides what is watched, this draws. Only the daemon's
	// fields are shown (counts, times, the end reason and its cause): a recording carries no device text.
	interface Props {
		model: UiRecordingModel;
		connection: ConnectionState;
		/** The panel's region is open. */
		visible: boolean;
	}
	let { model, connection, visible }: Props = $props();

	let view = $state.raw<UiRecordingView>(untrack(() => model.view));
	let pageHidden = $state(document.hidden);
	let now = $state(Date.now());

	$effect(() => {
		const m = model;
		const read = () => {
			view = m.view;
		};
		read();
		return m.subscribe(read);
	});

	// Watching costs the daemon nothing while the panel cannot be seen.
	$effect(() => model.setActive(visible && !pageHidden));
	$effect(() => model.onConnection(connection));

	// The elapsed time of a running recording ticks once a second, only while one runs and is seen.
	$effect(() => {
		if (view.running === null || !visible || pageHidden) return;
		now = Date.now();
		const h = setInterval(() => (now = Date.now()), 1000);
		return () => clearInterval(h);
	});

	const END: Record<string, string> = {
		stopped: 'stopped',
		maxDuration: 'time limit reached',
		maxDumps: 'dump limit reached',
		maxBytes: 'size limit reached',
		deviceGone: 'device went away',
		failed: 'failed',
		daemonExit: 'daemon exited',
	};

	function when(r: UiRecording): string {
		const t = new Date(r.startedAt);
		return Number.isNaN(t.getTime()) ? r.startedAt : t.toLocaleString();
	}
	function ended(r: UiRecording): string {
		if (r.state === 'recording') return 'recording';
		const why = r.endReason ? (END[r.endReason] ?? r.endReason) : 'ended';
		return r.cause ? `${why}: ${r.cause}` : why;
	}
	function counts(r: UiRecording): string {
		return `${r.dumps} dumps, ${r.events} events` + (r.missed > 0 ? `, ${r.missed} missed` : '') + (r.gaps > 0 ? `, ${r.gaps} gaps` : '');
	}
</script>

<svelte:document onvisibilitychange={() => (pageHidden = document.hidden)} />

<section class="rec">
	<div class="head">
		<h2>UI recording</h2>
		{#if view.running}
			<span class="muted small" role="status">{clockText(elapsedSeconds(view.running, now))} · {counts(view.running)}</span>
			<button type="button" class="btn-icon" disabled={view.busy} onclick={() => void model.stop()}>
				<Square size={12} /> Stop
			</button>
		{:else}
			<button type="button" class="btn-icon" disabled={view.busy || view.device === ''} onclick={() => void model.record()}>
				<Circle size={12} /> Record
			</button>
		{/if}
	</div>
	{#if view.device === ''}
		<p class="muted">Select a device first.</p>
	{/if}
	{#if view.state === 'failed'}
		<p role="status">
			The recordings could not be watched: {inert(view.cause)}
			<button type="button" class="btn-icon" onclick={() => model.restart()}>Retry</button>
		</p>
	{/if}
	{#if view.error !== ''}
		<p class="err" role="alert">{inert(view.error)}</p>
	{/if}
	{#if view.recordings.length > 0}
		<details>
			<summary>Recordings ({view.recordings.length})</summary>
			<ul class="list" aria-label="UI recordings, newest first">
				{#each view.recordings as r (r.id)}
					<li class="row">
						<span class="clock">{when(r)}</span>
						<span>{clockText(elapsedSeconds(r, now))}</span>
						<span class="muted">{ended(r)}</span>
						<span class="muted">{counts(r)}</span>
						<span class="actions">
							{#if canDownload(r)}
								<button type="button" class="btn-icon" disabled={view.downloading !== null} onclick={() => void model.downloadRecording(r.id)}>
									<Download size={12} /> Download
								</button>
							{/if}
							{#if r.state === 'ended'}
								{#if view.confirming === r.id}
									<span>Delete this recording?</span>
									<button type="button" class="btn-icon" disabled={view.busy} onclick={() => void model.confirmDelete()}>Delete</button>
									<button type="button" class="btn-icon" onclick={() => model.cancelDelete()}>Keep</button>
								{:else}
									<button type="button" class="btn-icon" onclick={() => model.askDelete(r.id)}>
										<Trash size={12} /> Delete
									</button>
								{/if}
							{/if}
						</span>
					</li>
				{/each}
			</ul>
		</details>
	{/if}
</section>

<style>
	.head {
		display: flex;
		align-items: center;
		gap: var(--sp-3);
	}
	.head :global(h2) {
		margin: 0;
		margin-right: auto;
	}
	.err {
		color: var(--danger, inherit);
	}
	.list {
		margin: var(--sp-3) 0 0;
		max-height: 200px;
		overflow: auto;
		padding: var(--sp-1);
		list-style: none;
		border: 1px solid var(--border);
		border-radius: var(--radius-control);
		background: var(--bg-window);
	}
	.row {
		display: flex;
		flex-wrap: wrap;
		align-items: baseline;
		gap: var(--sp-3);
		min-height: var(--row);
		padding: 0 var(--sp-3);
		font-size: var(--fs-mono);
	}
	.clock {
		color: var(--text-3);
	}
	.actions {
		margin-left: auto;
		display: flex;
		align-items: center;
		gap: var(--sp-2);
	}
</style>
