<script lang="ts">
	import type { DevicesModel, DevicesView, DeviceInfo } from './devices';
	import type { EmulatorsModel, EmulatorsView } from './emulators';
	import type { InstallModel, InstallView } from './install';
	import { failureText } from '../build/failure';
	import type { ActiveOp } from './mirror';

	// The devices panel (plan m2b step 5): the devices and the selected one (shared with
	// Logcat), the AVDs with Start and Stop, install and launch. Booting, stopping and
	// installing are the mirror's, so any client's operation shows with its client kind and
	// a Cancel (D11). Every field is a text node.
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

<section>
	<h2>Devices</h2>
	{#if dv && ev && iv}
		<p>
			<label>
				Device
				<select value={dv.selected} onchange={(e) => devices.select(e.currentTarget.value)}>
					<option value="">none</option>
					{#each dv.devices as d (d.device)}
						<option value={d.device} disabled={!d.online}>{deviceLabel(d)}</option>
					{/each}
				</select>
			</label>
			<button type="button" disabled={!connected} onclick={refresh}>Refresh</button>
			{#if dv.error}
				device.list failed: {failureText(dv.error)}
			{:else if dv.devices.length === 0}
				No device is connected.
			{/if}
		</p>

		<h3>Emulators</h3>
		{#if ev.loadError}
			<p>emulator.list failed: {failureText(ev.loadError)}</p>
		{/if}
		<p>
			<label>
				<input type="checkbox" checked={ev.headless} onchange={(e) => emulators.setHeadless(e.currentTarget.checked)} />
				Headless
			</label>
		</p>
		{#if ev.avds.length === 0}
			<p>No AVD listed.</p>
		{/if}
		<ul>
			{#each ev.avds as avd (avd.name)}
				<li>
					{avd.name}
					{avd.config?.displayName ?? ''}
					{avd.running ? `running as ${avd.running.device}` : 'not running'}
					{#if ev.preselected === avd.name}
						(this project's AVD: Start it)
					{/if}
					<button
						type="button"
						disabled={ev.starting || ev.mirror.booting !== null}
						onclick={() => void emulators.start(avd.name)}
					>
						Start
					</button>
					{#if avd.running}
						<button
							type="button"
							disabled={ev.mirror.stopping !== null}
							onclick={() => avd.running && void emulators.stop(avd.running.device)}
						>
							Stop
						</button>
					{/if}
				</li>
			{/each}
		</ul>
		{#if ev.startError}
			<p>Start refused: {failureText(ev.startError)}</p>
		{/if}
		{#if ev.stopError}
			<p>Stop refused: {failureText(ev.stopError)}</p>
		{/if}
		{#if ev.mirror.booting}
			{@const op = ev.mirror.booting}
			<p>
				{opText('Booting', op)}
				<button type="button" onclick={() => void emulators.mirror.cancel(op.operationId)}>Cancel</button>
			</p>
		{/if}
		{#if ev.mirror.stopping}
			{@const op = ev.mirror.stopping}
			<p>
				{opText('Stopping', op)}
				<button type="button" onclick={() => void emulators.mirror.cancel(op.operationId)}>Cancel</button>
			</p>
		{/if}

		<h3>App</h3>
		<p>
			{#if iv.build}
				Build selection: {iv.build.module} {iv.build.variant}.
			{:else}
				Open a workspace and choose a module and variant in the build panel to install.
			{/if}
			<button
				type="button"
				disabled={iv.installing || iv.mirror.installing !== null || iv.device === '' || iv.build === null}
				onclick={() => void install.install()}
			>
				Install
			</button>
		</p>
		{#if iv.mirror.installing}
			{@const op = iv.mirror.installing}
			<p>
				{opText('Installing', op)}
				<button type="button" onclick={() => void install.mirror.cancel(op.operationId)}>Cancel</button>
			</p>
		{/if}
		{#if iv.installError}
			<p>Install refused: {failureText(iv.installError)}</p>
		{/if}
		{#if iv.installed}
			<p>
				Installed {iv.installed.applicationId}, {iv.installed.apk}
				{iv.installed.versionCode === undefined ? '' : `versionCode ${iv.installed.versionCode}`}
			</p>
		{/if}
		<p>
			<label>
				Application id
				<input type="text" value={iv.applicationId} oninput={(e) => install.setApplicationId(e.currentTarget.value)} />
			</label>
			<button
				type="button"
				disabled={iv.launching || iv.device === '' || iv.applicationId.trim() === ''}
				onclick={() => void install.launch()}
			>
				Launch
			</button>
		</p>
		{#if iv.launchError}
			<p>Launch refused: {failureText(iv.launchError)}</p>
		{/if}
		{#if iv.launched}
			{#if iv.launched.pid === null}
				<p>Launched {iv.launched.applicationId}: no process observed. {iv.launched.reason ?? ''}</p>
			{:else}
				<p>Launched {iv.launched.applicationId}, pid {iv.launched.pid}, at {iv.launched.launchedAt}</p>
			{/if}
		{/if}
		{#if iv.mirror.cancelError}
			<p>Cancel failed: {iv.mirror.cancelError}</p>
		{/if}
	{:else}
		<p>Loading...</p>
	{/if}
</section>
