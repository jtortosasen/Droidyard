<script lang="ts">
	import type { DevicesModel, DevicesView } from './devices';
	import { deviceHolds, inUseText, type DeviceHold, type SessionsModel } from '../projects/sessions';
	import { deviceLine, deviceText, remoteHolderText } from '../toolbar/devicemenu';
	import { lentRows, type LendingModel, type LendingView } from './lending';
	import type { EmulatorsModel, EmulatorsView } from './emulators';
	import type { InstallModel, InstallView } from './install';
	import { failureText } from '../build/failure';
	import { targetText, type ActiveOp } from './mirror';
	import Play from '@lucide/svelte/icons/play';
	import RefreshCw from '@lucide/svelte/icons/refresh-cw';
	import Smartphone from '@lucide/svelte/icons/smartphone';
	import Square from '@lucide/svelte/icons/square';

	// The devices panel (plan m2b step 5): the toolbar's device as text (the choice is the
	// toolbar's, m2e D36), the AVDs with Start and Stop, install and launch. Booting,
	// stopping and installing are the mirror's, so any client's operation shows with its
	// client kind and a Cancel (D11). Every field is a text node. Compact, at the top of
	// the inspector (plan m2d step 10): the device and Refresh on one line, one row per AVD.
	interface Props {
		devices: DevicesModel;
		emulators: EmulatorsModel;
		install: InstallModel;
		connected: boolean;
		/** Who holds which device comes from its live rows (ADR-0094), not from device.list. */
		sessions: SessionsModel;
		/** What this machine lends (plan m2t): its entries in hosting.lend, and Stop lending. */
		lending: LendingModel;
	}
	let { devices, emulators, install, connected, sessions, lending }: Props = $props();

	let holds = $state.raw<DeviceHold[]>([]);
	let releaseError = $state<string | null>(null);
	$effect(() => {
		const s = sessions;
		const read = () => {
			holds = deviceHolds(s.view.rows, (id) => s.actorLabel(id));
		};
		read();
		return s.subscribe(read);
	});
	async function release(serial: string) {
		const f = await sessions.release({ device: serial });
		releaseError = f === null ? null : failureText(f);
	}

	let dv = $state.raw<DevicesView | null>(null);
	let ev = $state.raw<EmulatorsView | null>(null);
	let iv = $state.raw<InstallView | null>(null);
	let lv = $state.raw<LendingView | null>(null);

	$effect(() => {
		const d = devices;
		const read = () => {
			dv = d.view;
		};
		read();
		return d.subscribe(read);
	});
	$effect(() => {
		const l = lending;
		const read = () => {
			lv = l.view;
		};
		read();
		return l.subscribe(read);
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

	function opText(verb: string, op: ActiveOp): string {
		return `${verb}${targetText(op)} (${op.client ?? 'unknown client'}): ${op.capability}, ${op.state}.`;
	}

	function refresh() {
		void devices.refresh();
		void emulators.refresh();
		void lending.refresh();
	}

	// The devices lent to peers carry their own row (holder, Release, Stop lending); the others
	// a session holds keep the plain row.
	const lent = $derived(lentRows(lv?.lent ?? [], holds));
	const otherHolds = $derived(holds.filter((h) => !lent.some((l) => l.serial === h.serial)));
	// A remote device's host and holder, from device.list: the holder is on the host's machine.
	const remote = $derived((dv?.devices ?? []).filter((d) => d.host !== undefined));
</script>

<section class="devices">
	<h2>Devices</h2>
	{#if dv && ev && iv}
		<div class="line">
			<Smartphone size={14} />
			<span class="grow device" title="The toolbar's device">{deviceLine(dv)}</span>
			<button type="button" class="btn-icon" title="Refresh the device and emulator lists" aria-label="Refresh" disabled={!connected} onclick={refresh}>
				<RefreshCw size={14} />
			</button>
		</div>
		{#if dv.error}
			<p class="err small">device.list failed: {failureText(dv.error)}</p>
		{:else if dv.devices.length === 0}
			<p class="muted small">No device is connected.</p>
		{/if}
		{#each dv.warnings as w (w)}
			<p class="warn small" role="status">{w}</p>
		{/each}
		<!-- A device another Droidyard lends: its host, and who holds it there (ADR-0104). -->
		{#if remote.length > 0}
			<ul class="remotes" aria-label="Remote devices">
				{#each remote as d (d.device)}
					{@const holder = remoteHolderText(d)}
					<li class="line remote" data-device={d.device}>
						<span class="grow small" title={d.device}>{deviceText(d)}</span>
						{#if holder !== ''}<span class="holder small">{holder}</span>{/if}
					</li>
				{/each}
			</ul>
		{/if}
		<!-- Another chat holds these (ADR-0094): the owner's Release frees one and moves no session. -->
		{#each otherHolds as h (h.serial)}
			<div class="line held">
				<span class="grow small" title={h.serial}>{h.serial}: {inUseText(h)}</span>
				<button type="button" class="btn-icon" disabled={!connected} onclick={() => void release(h.serial)}>Release</button>
			</div>
		{/each}
		<!-- Lent to paired Droidyards (ADR-0099): the holder is a peer's link, Release ends its lease,
			Stop lending removes the entry and the peer's open views stop within 2 s (ADR-0104 §4). -->
		{#each lent as l (l.serial)}
			<div class="line lent" data-serial={l.serial}>
				<span class="grow small" title={l.serial}>
					{l.serial}: {l.text}{#if l.holder}, <span class="holder">{inUseText(l.holder)}</span>{/if}
				</span>
				{#if l.holder}
					<button type="button" class="btn-icon" aria-label="Release {l.serial}" disabled={!connected} onclick={() => void release(l.serial)}>Release</button>
				{/if}
				<button type="button" class="btn-icon" aria-label="Stop lending {l.serial}" disabled={!connected} onclick={() => void lending.stopLending(l.serial)}>
					Stop lending
				</button>
			</div>
		{/each}
		{#if releaseError}
			<p class="err small">Release refused: {releaseError}</p>
		{/if}
		{#if lv?.error}
			<p class="err small">Stop lending refused: {failureText(lv.error)}</p>
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
	.device {
		overflow-wrap: anywhere;
	}
	.remotes {
		list-style: none;
		margin: 0;
		padding: 0;
	}
	.remote {
		margin: var(--sp-1) 0;
	}
	.holder {
		color: var(--text-2);
	}
	.warn {
		color: var(--warning);
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
		letter-spacing: var(--tracking-label);
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
