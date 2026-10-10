<script lang="ts">
	import type { TrustModel, TrustView } from './trust';
	import { failureText } from '../build/failure';

	// Trust for an untrusted project (ADR-0045 §5): what it means, and the one click that
	// grants it. Every field is a text node.
	interface Props {
		model: TrustModel;
	}
	let { model }: Props = $props();

	let view = $state.raw<TrustView | null>(null);

	$effect(() => {
		const m = model;
		const read = () => {
			view = m.view;
		};
		read();
		return m.subscribe(read);
	});
</script>

<div>
	<p>Trusting lets this project's Gradle build scripts run as you.</p>
	<button type="button" disabled={!!view?.busy} onclick={() => void model.grant()}>Trust</button>
	{#if view?.busy}
		<p>Trusting...</p>
	{/if}
	{#if view?.conflict}
		<p>The policy changed while you were deciding. It was read again: click Trust to ask again.</p>
	{/if}
	{#if view?.error}
		<p>{failureText(view.error)}</p>
	{/if}
</div>
