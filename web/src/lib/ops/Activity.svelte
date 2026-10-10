<script lang="ts">
	import type { ActivityStore, Operation, WatchStatus } from './activity';

	// The activity feed: what every client runs in the daemon, newest first, from the
	// store (plan m2b step 2). Every field is a text node. The `ui` rows start hidden
	// because the UI's own reads are operations too (plan D16).
	interface Props {
		store: ActivityStore;
	}
	let { store }: Props = $props();

	const KINDS = ['cli', 'mcp', 'ui'] as const;

	let operations = $state.raw<readonly Operation[]>([]);
	let status = $state.raw<WatchStatus>({ state: 'idle', detail: '' });
	let shown = $state<Record<string, boolean>>({ cli: true, mcp: true, ui: false });

	$effect(() => {
		const s = store;
		const read = () => {
			operations = s.operations;
			status = s.status;
		};
		read();
		return s.subscribe(read);
	});

	// A call recorded without a kind has no checkbox and is always shown.
	const rows = $derived(operations.filter((o) => o.client === undefined || shown[o.client] !== false));

	function duration(ms: number | undefined): string {
		return ms === undefined ? '' : `${ms} ms`;
	}
</script>

<section>
	<h2>Activity</h2>
	<p>
		{#each KINDS as kind (kind)}
			<label>
				<input type="checkbox" bind:checked={shown[kind]} />
				{kind}
			</label>
		{/each}
		Watching: {status.state}{status.detail ? ` (${status.detail})` : ''}. {rows.length} of {operations.length} shown.
	</p>
	<table>
		<thead>
			<tr>
				<th>Started</th>
				<th>Client</th>
				<th>Capability</th>
				<th>Workspace</th>
				<th>State</th>
				<th>Duration</th>
				<th>Code</th>
			</tr>
		</thead>
		<tbody>
			{#each rows as o (o.id)}
				<tr>
					<td>{o.startedAt}</td>
					<td>{o.client ?? ''}</td>
					<td>{o.capability}</td>
					<td>{o.workspace ?? ''}</td>
					<td>{o.state === 'unseen' ? 'ended unseen' : o.state}</td>
					<td>{duration(o.durationMs)}</td>
					<td>{o.code ?? ''}</td>
				</tr>
			{/each}
		</tbody>
	</table>
</section>

<style>
	table {
		border-collapse: collapse;
	}
	th,
	td {
		text-align: left;
		padding: 0 0.75rem 0 0;
		font-family: ui-monospace, monospace;
		font-size: 0.85rem;
	}
</style>
