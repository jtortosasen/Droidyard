<script lang="ts">
	import type { Problems } from './build';

	// One row per error of the last build, from `build.errors`. Every field is text: the
	// messages quote compiler output and are never markup. When the daemon could not parse
	// the output it says so, and the labelled tail of the console is shown instead.
	interface Props {
		problems: Problems;
	}
	let { problems }: Props = $props();

	function where(e: Problems['errors'][number]): string {
		if (e.file === undefined) return '';
		return `${e.file}:${e.line ?? ''}:${e.column ?? ''}`;
	}
</script>

<h3>Problems</h3>
{#if problems.unrecognised}
	<p>The build output was not recognised; the end of the console follows.</p>
	<pre>{problems.tail ?? ''}</pre>
{:else if problems.errors.length === 0}
	<p>No problems.</p>
{/if}
{#if problems.errors.length > 0}
	<table>
		<thead>
			<tr>
				<th>Where</th>
				<th>Severity</th>
				<th>Tool</th>
				<th>Source</th>
				<th>Message</th>
			</tr>
		</thead>
		<tbody>
			{#each problems.errors as e, i (i)}
				<tr>
					<td>{where(e)}</td>
					<td>{e.severity ?? ''}</td>
					<td>{e.tool ?? ''}</td>
					<td>{e.source ?? ''}</td>
					<td>{e.message ?? ''}{e.merged ? ' (merged)' : ''}</td>
				</tr>
			{/each}
		</tbody>
	</table>
{/if}
{#if (problems.truncated ?? 0) > 0}
	<p>{problems.truncated} more not shown.</p>
{/if}

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
	pre {
		font-size: 0.85rem;
		overflow: auto;
	}
</style>
