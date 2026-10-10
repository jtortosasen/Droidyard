<script lang="ts">
	import type { DevicesModel, DevicesView, DeviceInfo } from './devices';
	import type { EmulatorsModel, EmulatorsView } from './emulators';
	import type { InstallModel, InstallView } from './install';
	import { failureText } from '../build/failure';
	import type { ActiveOp } from './mirror';
	import Play from '@lucide/svelte/icons/play';
	import RefreshCw from '@lucide/svelte/icons/refresh-cw';
	import Smartphone from '@lucide/svelte/icons/smartphone';
	import Square from '@lucide/svelte/icons/square';

	// The devices panel (plan m2b step 5): the devices and the selected one (shared with
	// Logcat), the AVDs with Start and Stop, install and launch. Booting, stopping and
	// installing are the mirror's, so any client's operation shows with its client kind and
	// a Cancel (D11). Every field is a text node. Compact, at the top of the inspector (plan
	// m2d step 10): the selector and Refresh on one line, one row per AVD with Start or Stop.
	interface Props {
		devices: DevicesModel;
		emulators: EmulatorsModel;
		install: InstallModel;
		connected: boolean;
	}
	let { devices, emulators, install, connected }: Props = $props();

	let dv = $state.raw<DevicesView | null>(null);
	let ev = $state.raw<EmulatorsView | null>(null);
	let iv = $state.raw<InstallView | null>(null);

	$effect(() => {
		const d = devices;
		const read = () => {
			dv = d.view;
		};
		read();
		return d.subscribe(read);
	});
	$effect(() => {
		const e = emulators;
		const read = () => {
			ev = e.view;
		};
		read();
		return e.subscribe(read);
	});
	$effect(() => {
		const i = install;
		const read = () => {
			iv = i.view;
		};
		read();
		return i.subscribe(read);
	});

	function deviceLabel(d: DeviceInfo): string {
		const name = d.avdName ?? d.model ?? d.product ?? d.kind;
		return `${d.device} (${name}, ${d.state})`;
	}

	function opText(verb: string, op: ActiveOp): string {
		return `${verb} (${op.client ?? 'unknown client'}): ${op.capability}, ${op.state}.`;
	}

	function refresh() {
		void devices.refresh();
		void emulators.refresh();
	}
</script>

<section class="devices">
	<h2>Devices</h2>
	{#if dv && ev && iv}
		<div class="line">
			<Smartphone size={14} />
			<select class="grow" aria-label="Device" value={dv.selected} onchange={(e) => devices.select(e.currentTarget.value)}>
				<option value="">none</option>
				{#each dv.devices as d (d.device)}
					<option value={d.device} disabled={!d.online}>{deviceLabel(d)}</option>
				{/each}
			</select>
			<button type="button" class="btn-icon" title="Refresh the device and emulator lists" aria-label="Refresh" disabled={!connected} onclick={refresh}>
				<RefreshCw size={14} />
			</button>
		</div>
		{#if dv.error}
			<p class="err small">device.list failed: {failureText(dv.error)}</p>
		{:else if dv.devices.length === 0}
			<p class="muted small">No device is connected.</p>
		{/if}

		<div class="head">
			<h3>Emulators</h3>
			<label class="check small">
				<input type="checkbox" checked={ev.headless} onchange={(e) => emulators.setHeadless(e.currentTarget.checked)} />
				Headless
			</label>
		</div>
		{#if ev.loadError}
			<p class="err small">emulator.list failed: {failureText(ev.loadError)}</p>
		{/if}
		{#if ev.avds.length === 0}
			<p class="muted small">No AVD listed.</p>
		{/if}
		<ul class="avds">
			{#each ev.avds as avd (avd.name)}
				<li class="avd">
					<span class="dot" class:on={!!avd.running} aria-hidden="true"></span>
					<span class="name">
						{avd.name}
						{#if avd.config?.displayName}<span class="muted">{avd.config.displayName}</span>{/if}
						<span class="muted small">{avd.running ? `running as ${avd.running.device}` : 'not running'}</span>
						{#if ev.preselected === avd.name}
							<span class="tag">this project's AVD</span>
						{/if}
					</span>
					{#if avd.running}
						<button
							type="button"
							class="btn-icon"
							disabled={ev.mirror.stopping !== null}
							onclick={() => avd.running && void emulators.stop(avd.running.device)}
						>
							<Square size={12} /> Stop
						</button>
					{/if}
					<button
						type="button"
						class="btn-icon"
						disabled={ev.starting || ev.mirror.booting !== null}
						onclick={() => void emulators.start(avd.name)}
					>
						<Play size={12} /> Start
					</button>
				</li>
			{/each}
		</ul>
		{#if ev.startError}
			<p class="err small">Start refused: {failureText(ev.startError)}</p>
		{/if}
		{#if ev.stopError}
			<p class="err small">Stop refused: {failureText(ev.stopError)}</p>
		{/if}
		{#if ev.mirror.booting}
			{@const op = ev.mirror.booting}
			<p class="op small">
				{opText('Booting', op)}
				<button type="button" onclick={() => void emulators.mirror.cancel(op.operationId)}>Cancel</button>
			</p>
		{/if}
		{#if ev.mirror.stopping}
			{@const op = ev.mirror.stopping}
			<p class="op small">
				{opText('Stopping', op)}
				<button type="button" onclick={() => void emulators.mirror.cancel(op.operationId)}>Cancel</button>
			</p>
		{/if}

		<div class="head">
			<h3>App</h3>
			<button
				type="button"
				class="btn-icon"
				disabled={iv.installing || iv.mirror.installing !== null || iv.device === '' || iv.build === null}
				onclick={() => void install.install()}
			>
				Install
			</button>
		</div>
		<p class="muted small">
			{#if iv.build}
				Build selection: {iv.build.module} {iv.build.variant}.
			{:else}
				Open a workspace and choose a module and variant in the build panel to install.
			{/if}
		</p>
		{#if iv.mirror.installing}
			{@const op = iv.mirror.installing}
			<p class="op small">
				{opText('Installing', op)}
				<button type="button" onclick={() => void install.mirror.cancel(op.operationId)}>Cancel</button>
			</p>
		{/if}
		{#if iv.installError}
			<p class="err small">Install refused: {failureText(iv.installError)}</p>
		{/if}
		{#if iv.installed}
			<p class="small">
				Installed {iv.installed.applicationId}, {iv.installed.apk}
				{iv.installed.versionCode === undefined ? '' : `versionCode ${iv.installed.versionCode}`}
			</p>
		{/if}
		<div class="line">
			<input
				type="text"
				class="grow"
				aria-label="Application id"
				placeholder="Application id"
				value={iv.applicationId}
				oninput={(e) => install.setApplicationId(e.currentTarget.value)}
			/>
			<button
				type="button"
				disabled={iv.launching || iv.device === '' || iv.applicationId.trim() === ''}
				onclick={() => void install.launch()}
			>
				Launch
			</button>
		</div>
		{#if iv.launchError}
			<p class="err small">Launch refused: {failureText(iv.launchError)}</p>
		{/if}
		{#if iv.launched}
			<p class="small">
				{#if iv.launched.pid === null}
					Launched {iv.launched.applicationId}: no process observed. {iv.launched.reason ?? ''}
				{:else}
					Launched {iv.launched.applicationId}, pid {iv.launched.pid}, at {iv.launched.launchedAt}
				{/if}
			</p>
		{/if}
		{#if iv.mirror.cancelError}
			<p class="err small">Cancel failed: {iv.mirror.cancelError}</p>
		{/if}
	{:else}
		<p class="muted">Loading...</p>
	{/if}
</section>

<style>
	.line {
		display: flex;
		align-items: center;
		gap: var(--sp-3);
		margin: var(--sp-3) 0;
		color: var(--text-2);
	}
	.grow {
		flex: 1;
		min-width: 0;
	}
	.head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--sp-3);
		margin-top: var(--sp-5);
	}
	.head h3 {
		margin: 0;
		font-size: var(--fs-small);
		font-weight: 600;
		color: var(--text-2);
		text-transform: uppercase;
		letter-spacing: 0.04em;
	}
	.check {
		display: inline-flex;
		align-items: center;
		gap: var(--sp-2);
		color: var(--text-2);
	}
	.avds {
		list-style: none;
		margin: var(--sp-2) 0;
		padding: 0;
	}
	.avd {
		display: flex;
		align-items: center;
		gap: var(--sp-3);
		min-height: var(--row);
		padding: var(--sp-1) var(--sp-3);
		border-radius: var(--radius-control);
	}
	.avd:hover {
		background: var(--bg-hover);
	}
	.name {
		flex: 1;
		min-width: 0;
		overflow-wrap: anywhere;
	}
	.dot {
		flex: none;
		width: 8px;
		height: 8px;
		border-radius: 50%;
		background: var(--text-3);
	}
	.dot.on {
		background: var(--success);
	}
	.tag {
		padding: 0 var(--sp-3);
		border-radius: var(--radius-control);
		background: var(--accent-soft);
		color: var(--accent);
		font-size: var(--fs-small);
	}
	.op {
		display: flex;
		align-items: center;
		gap: var(--sp-3);
	}
</style>
