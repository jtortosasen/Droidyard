<script lang="ts">
	import type { ActModel, ActView, ExpectRow } from './act';
	import { MAX_EXPECT, MAX_TEXT } from './act';
	import { failureText } from '../build/failure';
	import { quoted } from './text';
	import MousePointerClick from '@lucide/svelte/icons/mouse-pointer-click';
	import Keyboard from '@lucide/svelte/icons/keyboard';
	import Plus from '@lucide/svelte/icons/plus';
	import X from '@lucide/svelte/icons/x';

	// Tap and type with expectations on the selected device (plan m2b step 6). The target
	// is filled by picking a node in the tree or typed by hand. The answer is shown as lines
	// of text: no device field is anything but a text node. Nothing here repeats an action.
	// In the inspector (plan m2d step 10) the actions act on the node selected in the tree
	// above; the selector fields and the expectations are folded away until wanted.
	interface Props {
		model: ActModel;
		connected: boolean;
	}
	let { model, connected }: Props = $props();

	let view = $state.raw<ActView | null>(null);

	$effect(() => {
		const m = model;
		const read = () => {
			view = m.view;
		};
		read();
		return m.subscribe(read);
	});

	const kinds: ExpectRow['kind'][] = ['appears', 'gone'];
	const bys: ExpectRow['by'][] = ['text', 'id'];

	// The target in one line: the picked node's ref, or the typed fields, as quoted text.
	const target = $derived.by(() => {
		if (!view) return '';
		if (view.picked) return `node ${view.picked}`;
		const f = view.fields;
		const parts = [
			f.id !== '' ? `id ${quoted(f.id)}` : '',
			f.text !== '' ? `text ${quoted(f.text)}` : '',
			f.desc !== '' ? `desc ${quoted(f.desc)}` : '',
			f.cls !== '' ? `class ${quoted(f.cls)}` : '',
		].filter((p) => p !== '');
		return parts.join(', ');
	});
</script>

<section class="act">
	<h2>Tap and type</h2>
	{#if !view}
		<p class="muted">Loading...</p>
	{:else}
		{@const idle = connected && view.device !== '' && view.running === null}
		<div class="target">
			<MousePointerClick size={14} />
			{#if target !== ''}
				<span class="chip">{target}</span>
			{:else}
				<span class="muted small">No target: select a node in the tree, or open the selector below.</span>
			{/if}
			<button type="button" class="btn-primary" disabled={!idle} onclick={() => void model.tap()}>Tap</button>
		</div>
		{#if view.picked}
			<p class="muted small">Typing in a selector field replaces the picked node.</p>
		{/if}

		<details>
			<summary>Target selector</summary>
			<div class="fields">
				<label>
					Id
					<input type="text" value={view.fields.id} oninput={(e) => model.setField('id', e.currentTarget.value)} />
				</label>
				<label>
					Text
					<input type="text" value={view.fields.text} oninput={(e) => model.setField('text', e.currentTarget.value)} />
				</label>
				<label>
					Desc
					<input type="text" value={view.fields.desc} oninput={(e) => model.setField('desc', e.currentTarget.value)} />
				</label>
				<label>
					Class
					<input type="text" value={view.fields.cls} oninput={(e) => model.setField('cls', e.currentTarget.value)} />
				</label>
			</div>
		</details>

		<details>
			<summary>Expectations{view.expect.length > 0 ? ` (${view.expect.length})` : ''}</summary>
			{#each view.expect as row, i (i)}
				<div class="line">
					<select aria-label="Expectation kind" value={row.kind} onchange={(e) => model.setExpect(i, { kind: e.currentTarget.value === 'gone' ? 'gone' : 'appears' })}>
						{#each kinds as k (k)}
							<option value={k}>{k}</option>
						{/each}
					</select>
					<select aria-label="Expectation by" value={row.by} onchange={(e) => model.setExpect(i, { by: e.currentTarget.value === 'id' ? 'id' : 'text' })}>
						{#each bys as b (b)}
							<option value={b}>{b}</option>
						{/each}
					</select>
					<input type="text" class="grow" aria-label="Expectation value" value={row.value} oninput={(e) => model.setExpect(i, { value: e.currentTarget.value })} />
					<button type="button" class="btn-icon" title="Remove this expectation" aria-label="Remove" onclick={() => model.removeExpect(i)}><X size={12} /></button>
				</div>
			{/each}
			<div class="line">
				<button type="button" class="btn-icon" disabled={view.expect.length >= MAX_EXPECT} onclick={() => model.addExpect()}><Plus size={12} /> Add expectation</button>
				<label class="grow">
					Timeout (ms)
					<input type="text" inputmode="numeric" value={view.timeout} oninput={(e) => model.setTimeoutText(e.currentTarget.value)} />
				</label>
			</div>
		</details>

		<div class="line type">
			<Keyboard size={14} />
			<input
				type="text"
				class="grow"
				aria-label="Text to type (at most {MAX_TEXT} characters)"
				placeholder="Text to type (at most {MAX_TEXT} characters)"
				value={view.typeText}
				oninput={(e) => model.setTypeText(e.currentTarget.value)}
			/>
			<button type="button" disabled={!idle} onclick={() => void model.type()}>Type</button>
		</div>
		<div class="line options small">
			<label>
				<input type="checkbox" checked={view.clear} onchange={(e) => model.setClear(e.currentTarget.checked)} />
				Clear first
			</label>
			<label>
				<input type="checkbox" checked={view.submit} onchange={(e) => model.setSubmit(e.currentTarget.checked)} />
				Press Enter after
			</label>
		</div>

		{#if view.device === ''}
			<p class="muted small">Select a device first.</p>
		{/if}
		{#if view.running}
			<p class="muted small">Running {view.running === 'tap' ? 'the tap' : 'the typing'}...</p>
		{/if}
		{#if view.invalid}
			<p class="err small">Not sent: {view.invalid}</p>
		{/if}
		{#if view.error}
			<p class="err small">Refused: {failureText(view.error)}</p>
			{#if view.error.details}
				<pre>{view.error.details}</pre>
			{/if}
		{/if}
		{#if view.result}
			<div class="result" class:ok={view.result.matched !== false} class:unmet={view.result.matched === false}>
				<strong>{view.result.headline}</strong>
				<ul>
					{#each view.result.lines as line, i (i)}
						<li><pre>{line}</pre></li>
					{/each}
				</ul>
			</div>
		{/if}
	{/if}
</section>

<style>
	pre {
		display: inline;
		margin: 0;
		white-space: pre-wrap;
		overflow-wrap: anywhere;
	}
	ul {
		margin: var(--sp-2) 0 0;
		padding: 0;
	}
	li {
		list-style: none;
	}
	.target,
	.line {
		display: flex;
		align-items: center;
		gap: var(--sp-3);
		margin: var(--sp-3) 0;
		color: var(--text-2);
	}
	.target .chip {
		flex: 1;
		min-width: 0;
		padding: var(--sp-1) var(--sp-3);
		border-radius: var(--radius-control);
		background: var(--accent-soft);
		color: var(--text);
		font-family: var(--font-mono);
		font-size: var(--fs-mono);
		overflow-wrap: anywhere;
	}
	.target .muted {
		flex: 1;
	}
	.grow {
		flex: 1;
		min-width: 0;
	}
	.options {
		gap: var(--sp-5);
	}
	details {
		margin: var(--sp-2) 0;
	}
	summary {
		cursor: default;
		color: var(--text-2);
		font-size: var(--fs-small);
	}
	.fields {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: var(--sp-3);
		margin-top: var(--sp-3);
	}
	label {
		display: flex;
		flex-direction: column;
		gap: var(--sp-1);
		font-size: var(--fs-small);
		color: var(--text-2);
	}
	.options label {
		flex-direction: row;
		align-items: center;
		gap: var(--sp-2);
	}
	.result {
		margin-top: var(--sp-4);
		padding: var(--sp-3) var(--sp-4);
		border: 1px solid var(--border);
		border-left-width: 3px;
		border-radius: var(--radius-control);
		background: var(--bg-raised);
	}
	.result.ok {
		border-left-color: var(--success);
	}
	.result.unmet {
		border-left-color: var(--danger);
	}
</style>
