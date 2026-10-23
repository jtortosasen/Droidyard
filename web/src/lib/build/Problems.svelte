<script lang="ts">
	import type { Problems } from './build';
	import { locate, openProblem, type Position } from './locate';

	// One row per error of the last build, from `build.errors`. Every field is text: the
	// messages quote compiler output and are never markup. When the daemon could not parse
	// the output it says so, and the labelled tail of the console is shown instead.
	interface Props {
		problems: Problems;
		/** Chosen: a row the project holds (plan m2i D10); without it no row is a link. */
		onlocate?: (at: Position) => void;
	}
	let { problems, onlocate }: Props = $props();

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
					<td>
						{#if onlocate && locate(e)}
							<button type="button" class="where" title="Open in the editor" onclick={() => openProblem(e, onlocate)}>{where(e)}</button>
						{:else}
							{where(e)}
						{/if}
					</td>
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
		font-family: var(--font-mono);
		font-size: var(--fs-mono);
	}
	.where {
		min-height: 0;
		padding: 0;
		border: 0;
		background: transparent;
		color: var(--accent);
		font: inherit;
		text-decoration: underline;
		cursor: pointer;
	}
	pre {
		font-size: var(--fs-mono);
		overflow: auto;
	}
</style>
