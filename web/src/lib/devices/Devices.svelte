<script lang="ts">
	import type { DevicesModel, DevicesView } from './devices';
	import { deviceHolds, inUseText, type DeviceHold, type SessionsModel } from '../projects/sessions';
	import { deviceLine, deviceText, remoteHolderText } from '../toolbar/devicemenu';
	import { lentRows, type LendingModel, type LendingView } from './lending';
	import { rowActions, type EmulatorsModel, type EmulatorsView } from './emulators';
	import type { InstallModel, InstallView } from './install';
	import { failureText } from '../build/failure';
	import { targetText, type ActiveOp } from './mirror';
	import Play from '@lucide/svelte/icons/play';
	import RefreshCw from '@lucide/svelte/icons/refresh-cw';
	import Smartphone from '@lucide/svelte/icons/smartphone';
	import Square from '@lucide/svelte/icons/square';
	import { NUMBER_FIELDS, draftOf, draftProblems, emptyDraft, nameProblem, type Draft } from './avdform';

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
		/** Opens the SDK panel on system images (New AVD with no image installed). */
		onopensdk: () => void;
	}
	let { devices, emulators, install, connected, sessions, lending, onopensdk }: Props = $props();

	// New AVD, Edit, Wipe and Delete (plan m3f step 9). The forms hold text; the checks and
	// what is sent are avdform.ts's. Names, profiles and image ids come from the host or the
	// repository and are text nodes.
	let creating = $state(false);
	let newName = $state('');
	let newProfile = $state('');
	let newImage = $state('');
	let newDraft = $state<Draft>(emptyDraft());
	let editing = $state<string | null>(null);
	let editDraft = $state<Draft>(emptyDraft());
	let wiping = $state<string | null>(null);
	let deleting = $state<string | null>(null);

	function openNew() {
		creating = true;
		newName = '';
		newDraft = emptyDraft();
		void emulators.loadChoices();
	}
	async function submitNew() {
		const problems = await emulators.create({ name: newName, profile: newProfile, image: newImage, draft: newDraft });
		if (Object.keys(problems).length === 0 && emulators.view.createError === null) creating = false;
	}
	function openEdit(name: string) {
		const row = ev?.avds.find((a) => a.name === name);
		editing = name;
		editDraft = draftOf(row?.config?.settings);
		wiping = deleting = null;
	}
	async function submitEdit(name: string) {
		const problems = await emulators.configure(name, editDraft);
		if (Object.keys(problems).length === 0 && emulators.view.editError === null) editing = null;
	}
	async function confirmWipe(name: string) {
		await emulators.wipe(name);
		wiping = null;
	}
	async function confirmDelete(name: string) {
		await emulators.remove(name);
		if (emulators.view.deleteError === null) deleting = null;
	}

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
	const newProblems = $derived({
		...draftProblems(newDraft),
		...(newName === '' ? {} : { avd: nameProblem(newName, ev?.avds.map((a) => a.name) ?? []) }),
	});
	const editCurrent = $derived(ev?.avds.find((a) => a.name === editing)?.config?.settings);
	const editProblems = $derived(draftProblems(editDraft, editCurrent ?? {}));
	const problemList = (p: Record<string, string | undefined>) => Object.values(p).filter((x): x is string => !!x);
	const remote = $derived((dv?.devices ?? []).filter((d) => d.host !== undefined));
</script>

{#snippet settingsFields(draft: Draft, edit: boolean, problems: Record<string, string | undefined>, id: string)}
	<div class="fields">
		<label>
			Display name
			<input type="text" id="{id}-displayName" bind:value={draft.displayName} aria-invalid={!!problems.displayName} aria-describedby="{id}-displayName-r" />
			<span class="small muted" id="{id}-displayName-r">1 to 64 characters</span>
		</label>
		{#each NUMBER_FIELDS as f (f.key)}
			{#if !(f.createOnly && edit)}
				<label>
					{f.label}
					<input type="text" inputmode="numeric" id="{id}-{f.key}" bind:value={draft[f.key]} aria-invalid={!!problems[f.key]} aria-describedby="{id}-{f.key}-r" />
					<span class="small muted" id="{id}-{f.key}-r">{f.range}</span>
				</label>
			{/if}
		{/each}
		<label>
			Graphics
			<select bind:value={draft.graphics}>
				<option value="">default</option>
				<option value="automatic">automatic</option>
				<option value="hardware">hardware</option>
				<option value="software">software</option>
			</select>
		</label>
		<label>
			Orientation
			<select bind:value={draft.orientation}>
				<option value="">default</option>
				<option value="portrait">portrait</option>
				<option value="landscape">landscape</option>
			</select>
		</label>
		<label>
			Boot
			<select bind:value={draft.boot}>
				<option value="">default</option>
				<option value="quick">quick</option>
				<option value="cold">cold</option>
			</select>
		</label>
		<label>
			Device frame
			<select bind:value={draft.deviceFrame}>
				<option value="">default</option>
				<option value="true">shown</option>
				<option value="false">hidden</option>
			</select>
		</label>
		<label>
			Host keyboard
			<select bind:value={draft.keyboard}>
				<option value="">default</option>
				<option value="true">on</option>
				<option value="false">off</option>
			</select>
		</label>
	</div>
	{#each problemList(problems) as p (p)}
		<p class="err" role="alert">{p}</p>
	{/each}
{/snippet}

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
			<p class="err">device.list failed: {failureText(dv.error)}</p>
		{:else if dv.devices.length === 0}
			<p class="muted">No device is connected.</p>
		{/if}
		{#each dv.warnings as w (w)}
			<p class="warn" role="status">{w}</p>
		{/each}
		<!-- A device another Droidyard lends: its host, and who holds it there (ADR-0104). -->
		{#if remote.length > 0}
			<ul class="remotes" aria-label="Remote devices">
				{#each remote as d (d.device)}
					{@const holder = remoteHolderText(d)}
					<li class="line remote" data-device={d.device}>
						<span class="grow" title={d.device}>{deviceText(d)}</span>
						{#if holder !== ''}<span class="holder small">{holder}</span>{/if}
					</li>
				{/each}
			</ul>
		{/if}
		<!-- Another chat holds these (ADR-0094): the owner's Release frees one and moves no session. -->
		{#each otherHolds as h (h.serial)}
			<div class="line held">
				<span class="grow" title={h.serial}><span class="mono">{h.serial}</span>: {inUseText(h)}</span>
				<button type="button" class="btn-icon" disabled={!connected} onclick={() => void release(h.serial)}>Release</button>
			</div>
		{/each}
		<!-- Lent to paired Droidyards (ADR-0099): the holder is a peer's link, Release ends its lease,
			Stop lending removes the entry and the peer's open views stop within 2 s (ADR-0104 §4). -->
		{#each lent as l (l.serial)}
			<div class="line lent" data-serial={l.serial}>
				<span class="grow" title={l.serial}>
					<span class="mono">{l.serial}</span>: {l.text}{#if l.holder}, <span class="holder">{inUseText(l.holder)}</span>{/if}
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
			<p class="err">Release refused: {releaseError}</p>
		{/if}
		{#if lv?.error}
			<p class="err">Stop lending refused: {failureText(lv.error)}</p>
		{/if}

		<div class="head">
			<h3>Emulators</h3>
			<button type="button" class="btn-icon" disabled={!connected || creating} onclick={openNew}>New AVD</button>
			<label class="check small">
				<input type="checkbox" checked={ev.headless} onchange={(e) => emulators.setHeadless(e.currentTarget.checked)} />
				Headless
			</label>
		</div>
		{#if creating}
			<form
				class="form"
				aria-label="New AVD"
				onsubmit={(e) => {
					e.preventDefault();
					void submitNew();
				}}
			>
				<label>
					Name
					<input type="text" bind:value={newName} aria-invalid={!!newProblems.avd} autocomplete="off" />
				</label>
				<label>
					Device profile
					<select bind:value={newProfile}>
						<option value="">choose...</option>
						{#each ev.profiles as p (p.id)}
							<option value={p.id}>{p.name}{p.oem ? ` (${p.oem})` : ''}</option>
						{/each}
					</select>
				</label>
				<label>
					System image
					<select bind:value={newImage}>
						<option value="">choose...</option>
						{#each ev.images as img (img)}
							<option value={img}>{img}</option>
						{/each}
					</select>
				</label>
				{#if ev.choicesLoaded && ev.images.length === 0}
					<p class="muted" data-testid="no-image">
						No system image is installed.
						<button type="button" onclick={onopensdk}>Install one in the SDK panel</button>
					</p>
				{/if}
				{#if ev.choicesError}
					<p class="err">{failureText(ev.choicesError)}</p>
				{/if}
				<details>
					<summary>Advanced</summary>
					<p class="muted small">Empty keeps the device profile's and the image's default.</p>
					{@render settingsFields(newDraft, false, newProblems, 'new')}
				</details>
				{#if ev.createError}
					<p class="err">
						Create refused: {failureText(ev.createError)}
						{#if ev.createError.code === 'EMULATOR_IMAGE_NOT_INSTALLED'}
							<button type="button" onclick={onopensdk}>Install one in the SDK panel</button>
						{/if}
					</p>
				{/if}
				<div class="line">
					<button type="submit" class="primary" disabled={!connected || ev.working || problemList(newProblems).length > 0 || newName === '' || newProfile === '' || newImage === ''}>
						Create
					</button>
					<button type="button" onclick={() => (creating = false)}>Cancel</button>
				</div>
			</form>
		{/if}
		{#if ev.loadError}
			<p class="err">emulator.list failed: {failureText(ev.loadError)}</p>
		{/if}
		{#if ev.avds.length === 0}
			<p class="muted">No AVD listed.</p>
		{/if}
		<ul class="avds">
			{#each ev.avds as avd (avd.name)}
				{@const act = rowActions(avd, ev.working)}
				<li class="avd">
					<span class="dot" class:on={!!avd.running} aria-hidden="true"></span>
					<span class="name">
						<span class="mono line1" title={avd.name}>{avd.name}</span>
						{#if avd.config?.displayName}<span class="muted">{avd.config.displayName}</span>{/if}
						<span class="state small line1" class:on={!!avd.running} title={avd.running ? `running as ${avd.running.device}` : 'not running'}>{avd.running ? `running as ${avd.running.device}` : 'not running'}</span>
						{#if ev.preselected === avd.name}
							<span class="tag">this project's AVD</span>
						{/if}
					</span>
					{#if avd.running}
						<button
							type="button"
							class="btn-icon btn-danger stop"
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
					<button
						type="button"
						class="btn-icon"
						aria-label="Edit {avd.name}"
						disabled={!connected || !act.edit}
						title={avd.running ? 'Stop the AVD to edit it' : 'Edit its hardware settings'}
						onclick={() => openEdit(avd.name)}
					>
						Edit
					</button>
					<button
						type="button"
						class="btn-icon btn-danger apart"
						aria-label="Wipe {avd.name}"
						disabled={!connected || ev.working || ev.mirror.booting !== null}
						onclick={() => (wiping = avd.name)}
					>
						Wipe
					</button>
					<button
						type="button"
						class="btn-icon btn-danger"
						aria-label="Delete {avd.name}"
						disabled={!connected || !act.remove}
						title={avd.running ? 'Stop the AVD to delete it' : 'Delete the AVD'}
						onclick={() => (deleting = avd.name)}
					>
						Delete
					</button>
				</li>
				{#if act.why}
					<li class="note small muted" data-for={avd.name}>{act.why}</li>
				{/if}
				{#if editing === avd.name}
					<li class="form" aria-label="Edit {avd.name}">
						<p class="muted small">Prefilled with its current settings; only what you change is sent.</p>
						{@render settingsFields(editDraft, true, editProblems, 'edit')}
						{#if ev.editError}
							<p class="err">Edit refused: {failureText(ev.editError)}</p>
						{/if}
						<div class="line">
							<button type="button" class="primary" disabled={!connected || ev.working || problemList(editProblems).length > 0} onclick={() => void submitEdit(avd.name)}>Save</button>
							<button type="button" onclick={() => (editing = null)}>Cancel</button>
						</div>
					</li>
				{/if}
				{#if wiping === avd.name}
					<li class="form" role="group" aria-label="Confirm wipe">
						<p>Wipe <span class="mono">{avd.name}</span>? This erases its data and boots it.</p>
						<div class="line">
							<button type="button" class="btn-danger" disabled={!connected || ev.working} onclick={() => void confirmWipe(avd.name)}>Wipe</button>
							<button type="button" onclick={() => (wiping = null)}>Cancel</button>
						</div>
					</li>
				{/if}
				{#if deleting === avd.name}
					<li class="form" role="group" aria-label="Confirm delete">
						<p>Delete <span class="mono">{avd.name}</span>? The AVD and its data are removed.</p>
						<div class="line">
							<button type="button" class="btn-danger" disabled={!connected || ev.working || !!avd.running} onclick={() => void confirmDelete(avd.name)}>Delete</button>
							<button type="button" onclick={() => (deleting = null)}>Cancel</button>
						</div>
					</li>
				{/if}
			{/each}
		</ul>
		{#if ev.startError}
			<p class="err">Start refused: {failureText(ev.startError)}</p>
		{/if}
		{#if ev.wipeError}
			<p class="err">Wipe refused: {failureText(ev.wipeError)}</p>
		{/if}
		{#if ev.deleteError}
			<p class="err">Delete refused: {failureText(ev.deleteError)}</p>
		{/if}
		{#if ev.stopError}
			<p class="err">Stop refused: {failureText(ev.stopError)}</p>
		{/if}
		{#if ev.mirror.booting}
			{@const op = ev.mirror.booting}
			<p class="op">
				{opText('Booting', op)}
				<button type="button" onclick={() => void emulators.mirror.cancel(op.operationId)}>Cancel</button>
			</p>
		{/if}
		{#if ev.mirror.stopping}
			{@const op = ev.mirror.stopping}
			<p class="op">
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
		<p class="muted">
			{#if iv.build}
				Build selection: {iv.build.module} {iv.build.variant}.
			{:else}
				Open a workspace and choose a module and variant in the build panel to install.
			{/if}
		</p>
		{#if iv.mirror.installing}
			{@const op = iv.mirror.installing}
			<p class="op">
				{opText('Installing', op)}
				<button type="button" onclick={() => void install.mirror.cancel(op.operationId)}>Cancel</button>
			</p>
		{/if}
		{#if iv.installError}
			<p class="err">Install refused: {failureText(iv.installError)}</p>
		{/if}
		{#if iv.installed}
			<p>
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
			<p class="err">Launch refused: {failureText(iv.launchError)}</p>
		{/if}
		{#if iv.launched}
			<p>
				{#if iv.launched.pid === null}
					Launched {iv.launched.applicationId}: no process observed. {iv.launched.reason ?? ''}
				{:else}
					Launched {iv.launched.applicationId}, pid {iv.launched.pid}, at {iv.launched.launchedAt}
				{/if}
			</p>
		{/if}
		{#if iv.mirror.cancelError}
			<p class="err">Cancel failed: {iv.mirror.cancelError}</p>
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
		font-size: var(--fs-label);
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
	}
	/* The AVD name and the state word each keep one line and ellipsize; no token is cut in the middle. */
	.line1 {
		display: block;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
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
	.state {
		color: var(--text-2);
	}
	.state.on {
		color: var(--success);
	}
	.avd button,
	.line button,
	.head button {
		min-height: 24px;
		font-size: var(--fs-small);
	}
	/* Stop is destructive: set apart from Start and from the name (contract). */
	.stop {
		margin: 0 var(--sp-2);
	}
	/* Wipe and Delete are destructive: Wipe is set apart from Edit by a gap, Delete stands last. */
	.apart {
		margin-left: var(--sp-3);
	}
	.form {
		display: flex;
		flex-direction: column;
		gap: var(--sp-3);
		margin: var(--sp-3) 0;
		padding: var(--sp-3);
		border: 1px solid var(--border);
		border-radius: var(--radius-control);
		list-style: none;
	}
	.form label,
	.fields label {
		display: flex;
		flex-direction: column;
		gap: var(--sp-1);
		color: var(--text-2);
	}
	.fields {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
		gap: var(--sp-3);
	}
	.note {
		list-style: none;
		padding: 0 var(--sp-3);
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
