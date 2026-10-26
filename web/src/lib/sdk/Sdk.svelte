<script lang="ts">
	import { SdkModel, sizeText, type SdkView } from './sdk';
	import { failureText } from '../build/failure';
	import { inert } from '../devices/text';
	import RefreshCw from '@lucide/svelte/icons/refresh-cw';

	// The SDK manager (plan m3f step 8): what is installed and on offer, grouped by category;
	// Install and Update behind a confirm that shows sizes and, for each licence not yet
	// accepted, its text with the person's Accept (ADR-0015 §6); Uninstall behind a confirm
	// naming the space freed. An install or uninstall by any client shows with its client
	// kind and a Cancel. Names, licence texts and what uses a package come from the
	// repository or the host: every one is a text node, never markup.
	interface Props {
		sdk: SdkModel;
		connected: boolean;
	}
	let { sdk, connected }: Props = $props();

	let v = $state.raw<SdkView | null>(null);
	$effect(() => {
		const s = sdk;
		const read = () => {
			v = s.view;
		};
		read();
		return s.subscribe(read);
	});

	function updateText(update: string | undefined, size: number | undefined): string {
		return update === undefined ? '' : `update to ${inert(update)}, ${sizeText(size)}`;
	}
</script>

<section class="sdk">
	<div class="head">
		<h2>SDK</h2>
		<button type="button" class="btn-icon" title="Read the SDK and the repository again" aria-label="Refresh" disabled={!connected} onclick={() => void sdk.refresh(true)}>
			<RefreshCw size={14} />
		</button>
	</div>
	{#if v === null || !v.loaded}
		<p class="muted">Loading...</p>
	{:else}
		{#if v.loadError}
			<p class="err">{failureText(v.loadError)}</p>
		{/if}
		{#each v.warnings as w (w)}
			<p class="warn" role="status">{inert(w)}</p>
		{/each}
		{#if v.found}
			<p class="muted">SDK at <span class="mono">{inert(v.root ?? '')}</span></p>
		{:else}
			<div class="setup">
				<p>No Android SDK was found on this machine.</p>
				<p class="muted">
					Set up installs platform-tools into
					<span class="mono target">{v.targetRoot === undefined ? 'a directory that cannot be written' : inert(v.targetRoot)}</span>.
				</p>
				<button type="button" disabled={!connected || v.busy || v.targetRoot === undefined} onclick={() => sdk.setUp()}>Set up</button>
			</div>
		{/if}
		{#if v.repository?.state === 'failed'}
			<p class="err">
				The package repository could not be read{v.repository.cause ? `: ${inert(v.repository.cause)}` : ''}. Nothing can be installed until it can.
			</p>
		{:else if v.repository?.state === 'cached' && v.repository.cause}
			<p class="warn">Showing the copy kept earlier: {inert(v.repository.cause)}</p>
		{/if}
		{#if v.truncated}
			<p class="muted">More than 300 packages matched; only the first are listed.</p>
		{/if}

		{#if v.op}
			{@const op = v.op}
			<p class="op">
				{op.capability} ({op.client ?? 'unknown client'}): {op.state}.
				<button type="button" disabled={!connected} onclick={() => void sdk.cancel(op.operationId)}>Cancel</button>
			</p>
		{/if}
		{#if v.notice}<p role="status">{inert(v.notice)}</p>{/if}
		{#if v.error && !v.confirming && v.uninstalling === null}
			<p class="err">{failureText(v.error)}</p>
		{/if}

		{#if v.confirming}
			<div class="confirm" role="group" aria-label="Confirm install">
				<h3>Install</h3>
				<ul class="plan">
					{#each v.plan.items as it (it.id)}
						<li>
							<span class="mono">{inert(it.id)}</span>
							<span class="muted">{it.update ? 'update' : 'new'}{it.dependency ? ', dependency' : ''}, {sizeText(it.size)}</span>
						</li>
					{/each}
				</ul>
				<p>Download: {sizeText(v.plan.totalSize)}.</p>
				{#each v.plan.pending as l (l.id + l.sha1)}
					<div class="licence">
						<h4>Licence <span class="mono">{inert(l.id)}</span></h4>
						<p class="muted">Covers {l.packages.map(inert).join(', ')}.</p>
						{#if l.text === undefined}
							<p class="err">The licence text is not available; it cannot be accepted here.</p>
						{:else}
							<pre class="text">{l.text}</pre>
							<button type="button" disabled={!connected || v.busy} onclick={() => void sdk.accept(l.id, l.sha1)}>Accept this licence</button>
						{/if}
					</div>
				{/each}
				{#if v.plan.pending.length > 0}
					<p class="muted">Accept each licence above to enable Install.</p>
				{/if}
				{#if v.error}
					<p class="err">{failureText(v.error)}</p>
				{/if}
				{#if v.inUse.length > 0}
					<ul class="inuse" aria-label="Packages in use">
						{#each v.inUse as u (u.id)}
							<li>
								<span class="mono">{inert(u.id)}</span> is used by
								{u.usedBy.length === 0 ? 'something that could not be named' : u.usedBy.map(inert).join(', ')}
							</li>
						{/each}
					</ul>
				{/if}
				<div class="line">
					<button type="button" class="primary" disabled={!connected || !v.canRun} onclick={() => void sdk.run()}>Install</button>
					<button type="button" onclick={() => sdk.closeConfirm()}>Close</button>
				</div>
			</div>
		{/if}

		{#if v.uninstalling}
			{@const u = v.uninstalling}
			<div class="confirm" role="group" aria-label="Confirm uninstall">
				<h3>Uninstall</h3>
				<p>Remove <span class="mono">{inert(u.id)}</span>, freeing {sizeText(u.sizeOnDisk)} on disk.</p>
				{#if v.error}
					<p class="err">{failureText(v.error)}</p>
				{/if}
				{#if v.inUse.length > 0}
					<ul class="inuse" aria-label="Packages in use">
						{#each v.inUse as x (x.id)}
							<li>
								<span class="mono">{inert(x.id)}</span> is used by
								{x.usedBy.length === 0 ? 'something that could not be named' : x.usedBy.map(inert).join(', ')}
							</li>
						{/each}
					</ul>
				{/if}
				<div class="line">
					<button type="button" class="btn-danger" disabled={!connected || v.busy} onclick={() => void sdk.uninstall()}>Uninstall</button>
					<button type="button" onclick={() => sdk.closeUninstall()}>Close</button>
				</div>
			</div>
		{/if}

		{#if v.category !== null}
			<p class="muted">
				Showing only {v.groups[0]?.label ?? v.category}.
				<button type="button" onclick={() => sdk.setCategory(null)}>Show all</button>
			</p>
		{/if}
		<div class="line">
			<button type="button" disabled={!connected || v.busy || v.plan.items.length === 0} onclick={() => sdk.openConfirm()}>
				Install selected ({v.selected.length})
			</button>
		</div>

		{#each v.groups as g (g.category)}
			<h3 class="cat">{g.label}</h3>
			<ul class="rows">
				{#each g.rows as r (r.id)}
					<li class="row" data-package={r.id}>
						<input
							type="checkbox"
							aria-label={`Select ${r.id}`}
							disabled={!r.selectable || v.busy}
							checked={v.selected.includes(r.id)}
							onchange={() => sdk.toggle(r.id)}
						/>
						<span class="name">
							<span class="mono line1" title={r.id}>{inert(r.id)}</span>
							<span class="muted small line1">{inert(r.name)}</span>
						</span>
						<span class="ver small">
							{#if r.installed}
								{r.version === undefined ? 'installed' : inert(r.version)}
								{#if r.update !== undefined}<span class="tag">{updateText(r.update, r.size)}</span>{/if}
							{:else}
								{r.available === undefined ? '' : inert(r.available)}, {sizeText(r.size)}
							{/if}
						</span>
						{#if r.installed && r.update !== undefined}
							<button type="button" disabled={!connected || v.busy} onclick={() => sdk.update(r.id)}>Update</button>
						{/if}
						{#if r.installed}
							<button type="button" class="btn-danger" disabled={!connected || v.busy} onclick={() => sdk.openUninstall(r.id)}>Uninstall</button>
						{/if}
					</li>
				{/each}
			</ul>
		{/each}
	{/if}
</section>

<style>
	.sdk {
		padding: var(--sp-3);
	}
	.head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--sp-3);
	}
	.line {
		display: flex;
		align-items: center;
		gap: var(--sp-3);
		margin: var(--sp-3) 0;
		color: var(--text-2);
	}
	.warn {
		color: var(--warning);
	}
	.setup,
	.confirm {
		margin: var(--sp-3) 0;
		padding: var(--sp-3);
		border: 1px solid var(--border);
		border-radius: var(--radius-control);
	}
	.target {
		overflow-wrap: anywhere;
	}
	.plan,
	.inuse,
	.rows {
		list-style: none;
		margin: var(--sp-2) 0;
		padding: 0;
	}
	.text {
		max-height: 12rem;
		overflow: auto;
		margin: var(--sp-2) 0;
		padding: var(--sp-3);
		white-space: pre-wrap;
		overflow-wrap: anywhere;
		background: var(--bg-hover);
		color: var(--text);
	}
	.cat {
		margin: var(--sp-5) 0 var(--sp-2);
		font-size: var(--fs-label);
		font-weight: 600;
		color: var(--text-2);
		text-transform: uppercase;
		letter-spacing: var(--tracking-label);
	}
	.row {
		display: flex;
		align-items: center;
		gap: var(--sp-3);
		min-height: var(--row);
		padding: var(--sp-1) var(--sp-3);
		border-radius: var(--radius-control);
	}
	.row:hover {
		background: var(--bg-hover);
	}
	.name {
		flex: 1;
		min-width: 0;
	}
	.line1 {
		display: block;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.ver {
		color: var(--text-2);
	}
	.tag {
		padding: 0 var(--sp-3);
		border-radius: var(--radius-control);
		background: var(--accent-soft);
		color: var(--accent);
	}
	.row button {
		min-height: 24px;
		font-size: var(--fs-small);
	}
	.op {
		display: flex;
		align-items: center;
		gap: var(--sp-3);
	}
</style>
