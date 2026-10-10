<script lang="ts">
	import type { ActModel, ActView, ExpectRow } from './act';
	import { MAX_EXPECT, MAX_TEXT } from './act';
	import { failureText } from '../build/failure';

	// Tap and type with expectations on the selected device (plan m2b step 6). The target
	// is filled by picking a node in the tree or typed by hand. The answer is shown as lines
	// of text: no device field is anything but a text node. Nothing here repeats an action.
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
</script>

<section>
	<h2>Tap and type</h2>
	{#if !view}
		<p>Loading...</p>
	{:else}
		{@const idle = connected && view.device !== '' && view.running === null}
		<h3>Target</h3>
		{#if view.picked}
			<p>
				A picked node, by its ref {view.picked}. Typing in a field below replaces it.
			</p>
		{/if}
		<p>
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
		</p>

		<h3>Expectations</h3>
		{#each view.expect as row, i (i)}
			<p>
				<select value={row.kind} onchange={(e) => model.setExpect(i, { kind: e.currentTarget.value === 'gone' ? 'gone' : 'appears' })}>
					{#each kinds as k (k)}
						<option value={k}>{k}</option>
					{/each}
				</select>
				<select value={row.by} onchange={(e) => model.setExpect(i, { by: e.currentTarget.value === 'id' ? 'id' : 'text' })}>
					{#each bys as b (b)}
						<option value={b}>{b}</option>
					{/each}
				</select>
				<input type="text" value={row.value} oninput={(e) => model.setExpect(i, { value: e.currentTarget.value })} />
				<button type="button" onclick={() => model.removeExpect(i)}>Remove</button>
			</p>
		{/each}
		<p>
			<button type="button" disabled={view.expect.length >= MAX_EXPECT} onclick={() => model.addExpect()}>Add expectation</button>
			<label>
				Timeout (ms)
				<input type="text" inputmode="numeric" value={view.timeout} oninput={(e) => model.setTimeoutText(e.currentTarget.value)} />
			</label>
		</p>

		<h3>Actions</h3>
		<p>
			<button type="button" disabled={!idle} onclick={() => void model.tap()}>Tap</button>
		</p>
		<p>
			<label>
				Text to type (at most {MAX_TEXT} characters)
				<input type="text" value={view.typeText} oninput={(e) => model.setTypeText(e.currentTarget.value)} />
			</label>
			<label>
				<input type="checkbox" checked={view.clear} onchange={(e) => model.setClear(e.currentTarget.checked)} />
				Clear first
			</label>
			<label>
				<input type="checkbox" checked={view.submit} onchange={(e) => model.setSubmit(e.currentTarget.checked)} />
				Press Enter after
			</label>
			<button type="button" disabled={!idle} onclick={() => void model.type()}>Type</button>
		</p>
		{#if view.device === ''}
			<p>Select a device first.</p>
		{/if}
		{#if view.running}
			<p>Running {view.running === 'tap' ? 'the tap' : 'the typing'}...</p>
		{/if}
		{#if view.invalid}
			<p>Not sent: {view.invalid}</p>
		{/if}
		{#if view.error}
			<p>Refused: {failureText(view.error)}</p>
			{#if view.error.details}
				<pre>{view.error.details}</pre>
			{/if}
		{/if}
		{#if view.result}
			<p><strong>{view.result.headline}</strong></p>
			<ul>
				{#each view.result.lines as line, i (i)}
					<li><pre>{line}</pre></li>
				{/each}
			</ul>
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
	li {
		list-style: none;
	}
</style>
