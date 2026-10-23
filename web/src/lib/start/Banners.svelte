<script lang="ts">
	import type { WorkspaceModel, WorkspaceView } from '../build/workspace';
	import type { TrustModel, TrustView } from './trust';
	import { failureText } from '../build/failure';
	import ShieldAlert from '@lucide/svelte/icons/shield-alert';
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';

	// The strips under the toolbar (plan m2d step 2): the restricted-mode banner while the
	// project is not trusted (the same trust flow as before, ADR-0045 §5), and the full text
	// of a workspace failure, which the toolbar shows short. Every field is a text node.
	interface Props {
		workspace: WorkspaceModel;
		trust: TrustModel;
	}
	let { workspace, trust }: Props = $props();

	let view = $state.raw<WorkspaceView | null>(null);
	let trustView = $state.raw<TrustView | null>(null);

	$effect(() => {
		const m = workspace;
		const read = () => {
			view = m.view;
		};
		read();
		return m.subscribe(read);
	});
	$effect(() => {
		const m = trust;
		const read = () => {
			trustView = m.view;
		};
		read();
		return m.subscribe(read);
	});

	const untrusted = $derived(!!view?.info && !view.info.trusted && !view.closedByOther);
</script>

{#if untrusted}
	<div class="banner warn" role="status">
		<ShieldAlert size={14} />
		<span>This project is not trusted: its Gradle build scripts will not run.</span>
		{#if view?.info?.mainRoot}
			<span>This is a worktree: Trust trusts the project at {view.info.mainRoot}, and with it every worktree of it.</span>
		{/if}
		<button type="button" disabled={!!trustView?.busy} onclick={() => void trust.grant()}>Trust</button>
		{#if trustView?.busy}
			<span>Trusting...</span>
		{/if}
		{#if trustView?.conflict}
			<span>The policy changed while you were deciding. It was read again: click Trust to ask again.</span>
		{/if}
		{#if trustView?.error}
			<span class="bad">{failureText(trustView.error)}</span>
		{/if}
	</div>
{/if}
{#if view?.error}
	<div class="banner bad" role="alert">
		<TriangleAlert size={14} />
		<span>{failureText(view.error)}</span>
	</div>
{/if}

<style>
	.banner {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: var(--sp-4);
		padding: var(--sp-2) var(--sp-5);
		font-size: var(--fs-base);
		border-bottom: 1px solid var(--border);
	}
	.warn {
		background: var(--warning-soft);
	}
	.bad {
		color: var(--danger);
	}
	.banner.bad {
		background: var(--bg-raised);
	}
</style>
