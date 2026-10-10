<script lang="ts">
	import type { Client, ConnectionState } from '../api/client';
	import type { ActivityStore } from '../ops/activity';
	import Activity from '../ops/Activity.svelte';
	import Logcat from '../logcat/Logcat.svelte';
	import { BuildModel } from '../build/build';
	import Build from '../build/Build.svelte';
	import { TasksModel } from '../build/tasks';
	import Tasks from '../build/Tasks.svelte';
	import { ConsoleModel } from '../build/console';
	import Console from '../build/Console.svelte';
	import type { EventsHub } from '../build/hub';
	import type { Failure } from '../build/failure';
	import { WorkspaceModel } from '../build/workspace';
	import Workspace from '../build/Workspace.svelte';
	import { TrustModel } from './trust';
	import { DevicesMirror } from '../devices/mirror';
	import { DevicesModel } from '../devices/devices';
	import { EmulatorsModel } from '../devices/emulators';
	import { InstallModel } from '../devices/install';
	import Devices from '../devices/Devices.svelte';
	import { UiTreeModel } from '../devices/uitree';
	import Tree from '../devices/Tree.svelte';
	import { ActModel } from '../devices/act';
	import Act from '../devices/Act.svelte';
	import { AppLogsModel } from '../devices/applogs';
	import AppLogs from '../devices/AppLogs.svelte';

	// One project in one tab (ADR-0045 §2): the panels of the workspace the address's `w`
	// names, over the session App owns. App mounts this keyed on the id, so a different
	// project is a new instance, never a model re-pointed.
	interface Props {
		client: Client;
		activity: ActivityStore;
		hub: EventsHub;
		connection: ConnectionState;
		workspaceId: string;
		/** The daemon refused the id: the tab goes to the picker with the reason. */
		onmissing: (why: Failure) => void;
		onpicker: () => void;
	}
	let { client, activity, hub, connection, workspaceId, onmissing, onpicker }: Props = $props();

	let workspace = $state.raw<WorkspaceModel | null>(null);
	let trust = $state.raw<TrustModel | null>(null);
	let build = $state.raw<BuildModel | null>(null);
	let tasks = $state.raw<TasksModel | null>(null);
	let buildConsole = $state.raw<ConsoleModel | null>(null);
	let devices = $state.raw<DevicesModel | null>(null);
	let emulators = $state.raw<EmulatorsModel | null>(null);
	let install = $state.raw<InstallModel | null>(null);
	let uiTree = $state.raw<UiTreeModel | null>(null);
	let act = $state.raw<ActModel | null>(null);
	let appLogs = $state.raw<AppLogsModel | null>(null);
	// The selected device is the devices model's; Logcat follows it.
	let device = $state('');

	$effect(() => {
		const c = client;
		const store = activity;
		const callOn = {
			call: ((name, params, opts) => c.call(name, params, opts)) as Client['call'],
		};
		const ws = new WorkspaceModel(callOn, hub);
		const trusting = new TrustModel(callOn, ws);
		const builds = new BuildModel(callOn, store, hub, ws);
		const taskList = new TasksModel(callOn, builds.mirror, ws);
		const console_ = new ConsoleModel(
			{
				call: callOn.call,
				subscribe: (name, params, handlers, opts) => c.subscribe(name, params, handlers, opts),
			},
			builds,
			ws,
		);
		const mirror = new DevicesMirror(store, callOn);
		const deviceList = new DevicesModel(callOn, store);
		const emulatorList = new EmulatorsModel(callOn, store, mirror);
		const installer = new InstallModel(
			callOn,
			mirror,
			{
				selection: () => (ws.id === null ? null : { root: ws.root, module: builds.view.module, variant: builds.view.variant }),
				subscribe: (l) => builds.subscribe(l),
			},
			deviceList,
		);
		const tree = new UiTreeModel(callOn, deviceList, store);
		const actions = new ActModel(callOn, deviceList);
		const logs = new AppLogsModel(callOn, deviceList, installer, store);
		const offDevice = deviceList.subscribe(() => {
			device = deviceList.view.selected;
		});
		workspace = ws;
		trust = trusting;
		build = builds;
		tasks = taskList;
		buildConsole = console_;
		devices = deviceList;
		emulators = emulatorList;
		install = installer;
		uiTree = tree;
		act = actions;
		appLogs = logs;
		// The project is opened by the id from the address (workspace.open is idempotent);
		// after another client closes it the panel offers to reopen, it does not reopen.
		// A refusal by the daemon sends the tab to the picker; a lost connection does not.
		let gone = false;
		void ws.openById(workspaceId).then((ok) => {
			const why = ws.view.error;
			if (!ok && !gone && why?.code) onmissing(why);
		});
		return () => {
			gone = true;
			offDevice();
			logs.dispose();
			actions.dispose();
			tree.dispose();
			installer.dispose();
			emulatorList.dispose();
			deviceList.dispose();
			mirror.dispose();
			console_.dispose();
			taskList.dispose();
			builds.dispose();
			ws.dispose();
			workspace = null;
			trust = null;
			build = null;
			tasks = null;
			buildConsole = null;
			devices = null;
			emulators = null;
			install = null;
			uiTree = null;
			act = null;
			appLogs = null;
			device = '';
		};
	});

	// The session's state reaches the models that act on it: the console resumes after a
	// lost connection, and the device lists are read when the socket (re)opens.
	$effect(() => {
		buildConsole?.onConnection(connection);
	});
	$effect(() => {
		if (connection !== 'open' || !devices || !emulators) return;
		void devices.refresh();
		void emulators.refresh();
	});
</script>

{#if workspace && trust}
	<Workspace model={workspace} {trust} {onpicker} />
{/if}
{#if build}
	<Build model={build} />
{/if}
{#if tasks}
	<Tasks model={tasks} />
{/if}
{#if buildConsole}
	<Console model={buildConsole} />
{/if}

{#if devices && emulators && install}
	<Devices {devices} {emulators} {install} connected={connection === 'open'} />
{/if}

{#if uiTree && act}
	{@const tree = uiTree}
	{@const actions = act}
	<Tree
		model={tree}
		connected={connection === 'open'}
		onpick={(index) => {
			const s = tree.selectorAt(index);
			if (s) actions.pick(s);
		}}
	/>
	<Act model={actions} connected={connection === 'open'} />
{/if}

{#if appLogs}
	<AppLogs model={appLogs} connected={connection === 'open'} />
{/if}

<Logcat {client} {connection} {device} />
