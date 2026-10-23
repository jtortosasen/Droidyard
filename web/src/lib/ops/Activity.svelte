<script lang="ts">
	import { tick } from 'svelte';
	import Check from '@lucide/svelte/icons/check';
	import ChevronRight from '@lucide/svelte/icons/chevron-right';
	import X from '@lucide/svelte/icons/x';
	import Ban from '@lucide/svelte/icons/ban';
	import CircleHelp from '@lucide/svelte/icons/circle-help';
	import Trash from '@lucide/svelte/icons/trash';
	import Clock from '@lucide/svelte/icons/clock';
	import Spinner from '../projects/Spinner.svelte';
	import type { BuildModel } from '../build/build';
	import type { ConsoleModel, ConsoleView } from '../build/console';
	import { failureText } from '../build/failure';
	import type { SessionsModel } from '../projects/sessions';
	import type { ActivityStore, Operation, WatchStatus } from './activity';
	import { buildBlocks, elapsedText, type ActorFilter, type Block } from './blocks';
	import { isRecord, type CommandRecord, type CommandRecordsModel } from './commands';
	import { canRestore, EXPIRED_TEXT, itemText, type RestoreModel, type RestoreState } from './restore';
	import { deletedBy, deletedBySession, stateText, unaccounted, type TrashEntry, type TrashModel, type TrashRowState } from './trash';

	// The activity stream (plan m2d step 8, direction C): a block per operation, newest at
	// the bottom, following the stream unless the user scrolled up. Every field is a text
	// node. The `ui` reads are not shown (m2b D16); the projects panel's selected row
	// filters the stream (D30) and a chip says so.
	interface Props {
		store: ActivityStore;
		sessions: SessionsModel;
		console: ConsoleModel;
		build: BuildModel;
		/** Restore of a completed file.delete (plan m2j step 10); `workspaceId` is the project this tab shows. */
		restore: RestoreModel;
		/** The workspace's trash (ADR-0096): what was deleted before this tab's first operation, with Restore. */
		trash: TrashModel;
		/** The hook's records of an agent's Android commands (ADR-0063 §6). */
		records: CommandRecordsModel;
		workspaceId: string;
		/** Shows the Build output tab. */
		onopenoutput: () => void;
	}
	let { store, sessions, console: buildConsole, build, restore, trash, records, workspaceId, onopenoutput }: Props = $props();

	/** How many console lines an expanded block draws: the newest ones. */
	const LINES = 200;

	let operations = $state.raw<readonly Operation[]>([]);
	let status = $state.raw<WatchStatus>({ state: 'idle', detail: '' });
	let agentKey = $state<string | null>(null);
	// Every session the tab has seen, numbered in first-seen order (not the panel's labels), and
	// the selected session's own name, which is all a deleted entry's `by` carries.
	let actors = $state.raw<ReadonlyMap<string, string>>(new Map());
	let agentName = $state<string | undefined>(undefined);
	let consoleView = $state.raw<ConsoleView | null>(null);
	let startError = $state.raw<{ code?: string; message: string } | null>(null);
	let actor = $state<ActorFilter>('all');
	let open = $state<Record<string, boolean>>({});
	let restores = $state.raw<ReadonlyMap<string, RestoreState>>(new Map());
	let trashed = $state.raw<readonly TrashEntry[]>([]);
	let trashStates = $state.raw<ReadonlyMap<string, TrashRowState>>(new Map());
	let trashError = $state.raw<string | null>(null);
	let recorded = $state.raw<ReadonlyMap<string, CommandRecord>>(new Map());
	let now = $state(Date.now());
	let following = $state(true);
	let scroller = $state<HTMLDivElement>();

	$effect(() => {
		const s = store;
		const read = () => {
			operations = s.operations;
			status = s.status;
		};
		read();
		return s.subscribe(read);
	});
	$effect(() => {
		const m = sessions;
		const read = () => {
			agentKey = m.selectedKey;
			actors = m.actorLabels();
			agentName = agentKey === null ? undefined : m.rawName(agentKey);
		};
		read();
		return m.subscribe(read);
	});
	$effect(() => {
		const m = buildConsole;
		const read = () => {
			consoleView = m.view;
		};
		read();
		return m.subscribe(read);
	});
	$effect(() => {
		const m = build;
		const read = () => {
			const e = m.view.startError;
			startError = e === null ? null : { code: e.code, message: failureText(e) };
		};
		read();
		return m.subscribe(read);
	});
	$effect(() => {
		const m = restore;
		const read = () => {
			restores = new Map(operations.map((o) => [o.id, m.stateOf(o.id)] as const).filter((e): e is [string, RestoreState] => e[1] !== undefined));
		};
		read();
		return m.subscribe(read);
	});
	// The trash is read when the watch starts and each time it starts again (a reconnect).
	$effect(() => {
		if (status.state === 'watching') void trash.load();
	});
	$effect(() => {
		const m = trash;
		const read = () => {
			trashed = m.entries;
			trashError = m.error;
			trashStates = new Map(m.entries.flatMap((e) => {
				const s = m.stateOf(e.id);
				return s === undefined ? [] : [[e.id, s] as const];
			}));
		};
		read();
		return m.subscribe(read);
	});
	// A completed `agent.recordCommand` or `agent.recordEdit` row has its result read once; the model caches it.
	$effect(() => {
		const m = records;
		for (const o of operations) if (isRecord(o.capability) && o.state === 'completed') void m.load(o.id, o.capability);
		const read = () => {
			recorded = new Map(
				operations.flatMap((o) => {
					const r = m.recordOf(o.id);
					return r === undefined ? [] : [[o.id, r] as const];
				}),
			);
		};
		read();
		return m.subscribe(read);
	});
	// Durations tick while anything is live.
	$effect(() => {
		const timer = setInterval(() => (now = Date.now()), 1000);
		return () => clearInterval(timer);
	});

	const labels = $derived(actors);
	const chip = $derived(agentKey === null ? null : (actors.get(agentKey) ?? 'agent'));
	const blocks = $derived(
		buildBlocks(
			operations,
			{ actor, agentKey },
			{ labels, records: recorded, heldBuild: consoleView?.hasBuild ? consoleView.buildId : null, ownFailure: startError },
		),
	);

	// The entries no operation of the store accounts for, oldest first like the stream, under
	// the same filters: an entry's name makes it an agent's; a selected agent keeps the entries
	// of its label alone.
	const deleted = $derived(
		unaccounted(trashed, operations, workspaceId)
			.filter((e) => {
				const who = deletedBy(e);
				if (actor === 'you' && !who.you) return false;
				if (actor === 'agents' && who.you) return false;
				return agentKey === null || deletedBySession(e, agentName);
			})
			.reverse(),
	);

	// Follow the bottom as blocks arrive, unless the user scrolled up.
	$effect(() => {
		void blocks.length;
		void deleted.length;
		void consoleView?.count;
		if (!following) return;
		void tick().then(() => {
			if (!scroller) return;
			scroller.scrollTop = scroller.scrollHeight;
		});
	});
	// A tab shown again (its pane was hidden, so its height was 0) lands on the bottom too.
	$effect(() => {
		const el = scroller;
		if (!el) return;
		const ro = new ResizeObserver(() => {
			if (following) el.scrollTop = el.scrollHeight;
		});
		ro.observe(el);
		return () => ro.disconnect();
	});
	function onscroll() {
		if (!scroller) return;
		following = scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < 24;
	}

	/** The time of day; with the date too when it is not today. */
	function whenText(iso: string, at: number): string {
		const d = new Date(iso);
		return new Date(at).toDateString() === d.toDateString() ? d.toLocaleTimeString() : d.toLocaleString();
	}

	function lines(): string[] {
		const v = consoleView;
		if (!v) return [];
		return buildConsole.rows(Math.max(0, v.count - LINES), v.count).map((r) => r.text);
	}
</script>

{#snippet icon(b: Block)}
	{#if b.tone === 'running'}
		<Spinner />
	{:else if b.tone === 'success'}
		<Check size={14} />
	{:else if b.tone === 'failure'}
		<X size={14} />
	{:else if b.tone === 'cancelled'}
		<Ban size={14} />
	{:else if b.tone === 'queued'}
		<Clock size={14} />
	{:else}
		<CircleHelp size={14} />
	{/if}
{/snippet}

<section class="activity">
	<h2>Activity</h2>
	<div class="filters">
		<div class="seg" role="group" aria-label="Who">
			{#each ['all', 'you', 'agents'] as const as k (k)}
				<button type="button" class:current={actor === k} aria-pressed={actor === k} onclick={() => (actor = k)}>
					{k === 'all' ? 'All' : k === 'you' ? 'You' : 'Agents'}
				</button>
			{/each}
		</div>
		{#if chip !== null}
			<span class="chip">
				{chip}
				<button type="button" class="clear" aria-label="Show every agent" title="Show every agent" onclick={() => sessions.select(null)}>
					<X size={12} />
				</button>
			</span>
		{/if}
		{#if trashError !== null}
			<span class="note">Trash: {trashError}</span>
		{/if}
		{#if status.state !== 'watching'}
			<span class="note">Watching: {status.state}{status.detail ? ` (${status.detail})` : ''}</span>
		{/if}
	</div>
	<div class="stream" bind:this={scroller} {onscroll}>
		{#each deleted as e (e.id)}
			{@const who = deletedBy(e)}
			{@const st = trashStates.get(e.id)}
			<article class="block" data-tone="deleted">
				<div class="head">
					<span class="status" data-tone="deleted" title="deleted"><Trash size={14} /></span>
					<span class="title">Deleted</span>
					<span class="actor" class:you={who.you}>{who.label}</span>
					<span class="target">{e.path}</span>
					<span class="time">{whenText(e.deletedAt, now)}</span>
				</div>
				<div class="restore">
					{#if st === undefined || st.kind === 'failed'}
						<button type="button" class="link" onclick={() => void trash.restore(e.id)}>Restore</button>
					{/if}
					{#if st !== undefined}
						<p class="why" class:err={st.kind === 'failed'}>{stateText(st)}</p>
					{/if}
				</div>
			</article>
		{/each}
		{#each blocks as b (b.id)}
			{@const expandable = b.output.kind !== 'none'}
			<article class="block" data-tone={b.tone}>
				<div class="head">
					<span class="status" data-tone={b.tone} title={b.stateText}>{@render icon(b)}</span>
					<span class="title" title={b.capability}>{b.title}</span>
					<span class="actor" class:you={b.you}>{b.actor}</span>
					{#if b.target !== ''}
						<span class="target">{b.target}</span>
					{/if}
					<span class="time">{new Date(b.startedAt).toLocaleTimeString()}</span>
					<span class="elapsed">{elapsedText(b, now)}</span>
					{#if expandable}
						<button
							type="button"
							class="chevron"
							class:turned={open[b.id]}
							aria-expanded={open[b.id] === true}
							aria-label="Output of {b.title}"
							onclick={() => (open[b.id] = !open[b.id])}
						>
							<ChevronRight size={14} />
						</button>
					{/if}
				</div>
				{#if b.tone === 'queued' && b.blockedBy}
					<p class="why">
						Waiting for {b.blockedBy.capability ?? 'another operation'}{b.blockedBy.reason ? `: ${b.blockedBy.reason}` : ''}
					</p>
				{/if}
				{#if b.command?.suggested}
					<p class="why">droidyard: {b.command.suggested}</p>
				{/if}
				{#if canRestore(b, workspaceId)}
					{@const r = restores.get(b.id)}
					<div class="restore">
						{#if r === undefined || r.kind === 'failed'}
							<button type="button" class="link" onclick={() => void restore.restore(b.id)}>Restore</button>
						{/if}
						{#if r?.kind === 'working'}
							<p class="why">Restoring…</p>
						{:else if r?.kind === 'nothing'}
							<p class="why">Nothing to restore: no item of this delete was done.</p>
						{:else if r?.kind === 'expired'}
							<p class="why">{EXPIRED_TEXT}</p>
						{:else if r?.kind === 'failed'}
							<p class="why err">{r.code}: {r.message}</p>
						{:else if r?.kind === 'done'}
							{#each r.items as item (item.path)}
								<p class="why" class:err={item.code !== undefined}>{itemText(item)}</p>
							{/each}
						{/if}
					</div>
				{/if}
				{#if open[b.id]}
					{#if b.output.kind === 'console'}
						<pre class="out">{lines().join('\n')}</pre>
					{:else if b.output.kind === 'error'}
						<p class="out err">{b.output.code}{b.output.message ? `: ${b.output.message}` : ''}</p>
					{:else if b.output.kind === 'build-output'}
						<p class="out"><button type="button" class="link" onclick={onopenoutput}>Open in Build output</button></p>
					{/if}
				{/if}
			</article>
		{:else}
			{#if deleted.length === 0}
				<p class="note">Nothing here yet.</p>
			{/if}
		{/each}
	</div>
</section>

<style>
	.activity {
		display: flex;
		flex-direction: column;
		flex: 1;
		min-height: 0;
		gap: var(--sp-3);
	}
	.filters {
		flex: none;
		display: flex;
		align-items: center;
		gap: var(--sp-4);
	}
	.seg {
		display: inline-flex;
		padding: var(--sp-1);
		border: 1px solid var(--border);
		border-radius: var(--radius-control);
		background: var(--bg-window);
	}
	.seg button {
		min-height: 0;
		height: calc(var(--row) - 4px);
		border: 0;
		background: transparent;
		color: var(--text-2);
		font-size: var(--fs-small);
	}
	.seg button.current {
		background: var(--bg-active);
		color: var(--text);
	}
	.chip {
		display: inline-flex;
		align-items: center;
		gap: var(--sp-2);
		padding: 0 var(--sp-3);
		border-radius: 999px;
		background: var(--accent-soft);
		color: var(--accent);
		font-size: var(--fs-small);
	}
	.clear {
		display: grid;
		place-items: center;
		min-height: 0;
		padding: 0;
		border: 0;
		background: transparent;
		color: inherit;
	}
	.note {
		margin: 0;
		color: var(--text-3);
		font-size: var(--fs-small);
	}
	.stream {
		flex: 1;
		min-height: 54px;
		overflow: auto;
		overscroll-behavior: contain;
		display: flex;
		flex-direction: column;
		gap: var(--sp-3);
	}
	.block {
		flex: none;
		border: 1px solid var(--border);
		border-left: 3px solid var(--text-3);
		border-radius: var(--radius-panel);
		background: var(--bg-raised);
		padding: var(--sp-3) var(--sp-4);
	}
	.block[data-tone='running'],
	.block[data-tone='queued'] {
		border-left-color: var(--accent);
	}
	.block[data-tone='success'] {
		border-left-color: var(--success);
	}
	.block[data-tone='failure'] {
		border-left-color: var(--danger);
	}
	.block[data-tone='deleted'] {
		border-left-color: var(--text-3);
	}
	.head {
		display: flex;
		align-items: center;
		gap: var(--sp-4);
		min-height: var(--row);
	}
	.status {
		display: inline-grid;
		place-items: center;
		color: var(--text-3);
	}
	.status[data-tone='running'],
	.status[data-tone='queued'] {
		color: var(--accent);
	}
	.status[data-tone='success'] {
		color: var(--success);
	}
	.status[data-tone='failure'] {
		color: var(--danger);
	}
	.title {
		font-weight: 600;
	}
	.actor {
		padding: 0 var(--sp-3);
		border-radius: 999px;
		background: var(--bg-active);
		color: var(--text-2);
		font-family: var(--font-mono);
		font-size: var(--fs-small);
	}
	.actor.you {
		background: transparent;
		border: 1px solid var(--border);
	}
	.target,
	.time,
	.elapsed {
		color: var(--text-3);
		font-family: var(--font-mono);
		font-size: var(--fs-small);
		font-variant-numeric: tabular-nums;
	}
	.elapsed {
		margin-left: auto;
	}
	.chevron {
		display: grid;
		place-items: center;
		min-height: 0;
		padding: 0;
		border: 0;
		background: transparent;
		color: var(--text-2);
		transition: transform 0.12s;
	}
	.chevron.turned {
		transform: rotate(90deg);
	}
	.why {
		margin: var(--sp-2) 0 0;
		color: var(--text-2);
		font-size: var(--fs-small);
	}
	.restore {
		margin-top: var(--sp-2);
	}
	.out {
		margin: var(--sp-3) 0 0;
		max-height: 240px;
		overflow: auto;
		font-family: var(--font-mono);
		font-size: var(--fs-mono);
		white-space: pre-wrap;
		overflow-wrap: anywhere;
	}
	.err {
		color: var(--danger);
	}
	.link {
		min-height: 0;
		padding: 0;
		border: 0;
		background: transparent;
		color: var(--accent);
		text-decoration: underline;
		font: inherit;
	}
</style>
