<script lang="ts">
	import type { Client, ConnectionState } from '../api/client';
	import type { ActivityStore } from '../ops/activity';
	import Activity from '../ops/Activity.svelte';
	import Logcat from '../logcat/Logcat.svelte';
	import { BuildModel } from '../build/build';
	import Build from '../build/Build.svelte';
	import BuildResult from '../build/BuildResult.svelte';
	import { TasksModel } from '../build/tasks';
	import Tasks from '../build/Tasks.svelte';
	import { ConsoleModel } from '../build/console';
	import Console from '../build/Console.svelte';
	import type { EventsHub } from '../build/hub';
	import type { Failure } from '../build/failure';
	import { WorkspaceModel } from '../build/workspace';
	import Workspace from '../build/Workspace.svelte';
	import { TrustModel } from './trust';
	import { ProjectFileModel } from './projectfile';
	import { bindProjectFile } from './projectbind';
	import ProjectFile from './ProjectFile.svelte';
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
	import { FileTreeModel } from '../files/tree';
	import { FileWatch } from '../files/watch';
	import FileTree from '../files/FileTree.svelte';
	import Banners from './Banners.svelte';
	import Shell from '../ui/Shell.svelte';
	import type { Snippet } from 'svelte';

	// One project's panels (ADR-0047): the workspace the address's `w` names, over the
	// session App owns. App mounts this keyed on the id, so a different
	// project is a new instance, never a model re-pointed.
	interface Props {
		client: Client;
		activity: ActivityStore;
		hub: EventsHub;
		connection: ConnectionState;
		workspaceId: string;
		/** The daemon refused the id: the page shows the projects panel with the reason. */
		onmissing: (why: Failure) => void;
		/** "All projects": opens the projects panel. */
		onprojects: () => void;
		/** The connection and health line App owns, shown in the toolbar. */
		status: Snippet;
		/** The rail's projects icon, which App owns (it outlives the project view). */
		rail: Snippet;
	}
	let { client, activity, hub, connection, workspaceId, onmissing, onprojects, status, rail }: Props = $props();

	let workspace = $state.raw<WorkspaceModel | null>(null);
	let trust = $state.raw<TrustModel | null>(null);
	let projectFile = $state.raw<ProjectFileModel | null>(null);
	let build = $state.raw<BuildModel | null>(null);
	let tasks = $state.raw<TasksModel | null>(null);
	let buildConsole = $state.raw<ConsoleModel | null>(null);
	let devices = $state.raw<DevicesModel | null>(null);
	let emulators = $state.raw<EmulatorsModel | null>(null);
	let install = $state.raw<InstallModel | null>(null);
	let uiTree = $state.raw<UiTreeModel | null>(null);
	let act = $state.raw<ActModel | null>(null);
	let appLogs = $state.raw<AppLogsModel | null>(null);
	let fileTree = $state.raw<FileTreeModel | null>(null);
	let fileWatch = $state.raw<FileWatch | null>(null);
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
				// D20: the newest build's output for the chosen module and variant.
				output: (module, variant) => builds.view.last?.outputs?.find((o) => o.module === module && o.variant === variant)?.applicationId,
			},
			deviceList,
		);
		// ADR-0046: the project file preselects once it is read and is written from the
		// user's choices; nothing is written on load.
		const file = new ProjectFileModel(callOn, ws);
		const unbind = bindProjectFile({ file, build: builds, devices: deviceList, emulators: emulatorList, install: installer });
		const tree = new UiTreeModel(callOn, deviceList, store);
		const actions = new ActModel(callOn, deviceList);
		const logs = new AppLogsModel(callOn, deviceList, installer, store);
		const files = new FileTreeModel(callOn, ws);
		const watcher = new FileWatch(
			{
				call: callOn.call,
				subscribe: (name, params, handlers, opts) => c.subscribe(name, params, handlers, opts),
			},
			ws,
			files.sink,
		);
		const offDevice = deviceList.subscribe(() => {
			device = deviceList.view.selected;
		});
		workspace = ws;
		trust = trusting;
		projectFile = file;
		build = builds;
		tasks = taskList;
		buildConsole = console_;
		devices = deviceList;
		emulators = emulatorList;
		install = installer;
		uiTree = tree;
		act = actions;
		appLogs = logs;
		fileTree = files;
		fileWatch = watcher;
		// The project is opened by the id from the address (workspace.open is idempotent);
		// after another client closes it the panel offers to reopen, it does not reopen.
		// A refusal by the daemon shows the projects panel with the reason; a lost connection does not.
		let gone = false;
		void ws.openById(workspaceId).then((ok) => {
			const why = ws.view.error;
			if (!ok && !gone && why?.code) onmissing(why);
		});
		return () => {
			gone = true;
			offDevice();
			watcher.dispose();
			files.dispose();
			unbind();
			file.dispose();
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
			projectFile = null;
			build = null;
			tasks = null;
			buildConsole = null;
			devices = null;
			emulators = null;
			install = null;
			uiTree = null;
			act = null;
			appLogs = null;
			fileTree = null;
			fileWatch = null;
			device = '';
		};
	});

	// The session's state reaches the models that act on it: the console resumes after a
	// lost connection, and the device lists are read when the socket (re)opens.
	$effect(() => {
		buildConsole?.onConnection(connection);
		projectFile?.onConnection(connection);
		fileWatch?.onConnection(connection);
	});
	$effect(() => {
		if (connection !== 'open' || !devices || !emulators) return;
		void devices.refresh();
		void emulators.refresh();
	});
</script>

{#snippet panel(id: string)}
	{#if id === 'workspace'}
		{#if workspace && trust}
			<Workspace model={workspace} {trust} {onprojects} compact />
		{/if}
	{:else if id === 'projectfile'}
		{#if projectFile}
			<ProjectFile model={projectFile} compact />
		{/if}
	{:else if id === 'build'}
		{#if build}
			<Build model={build} compact />
		{/if}
	{:else if id === 'files'}
		{#if fileTree && fileWatch}
			<!-- Step 6 wires the viewer; until then a chosen file is only selected. -->
			<FileTree model={fileTree} watch={fileWatch} onopen={() => {}} />
		{/if}
	{:else if id === 'editor'}
		<!-- Slot for the second-level file tabs (plan m2d step 6). -->
		<p class="empty">Open a file from the tree to read it here.</p>
	{:else if id === 'logcat'}
		<Logcat {client} {connection} {device} />
	{:else if id === 'applogs'}
		{#if appLogs}
			<AppLogs model={appLogs} connected={connection === 'open'} />
		{/if}
	{:else if id === 'console'}
		{#if buildConsole}
			<Console model={buildConsole} />
		{/if}
	{:else if id === 'problems'}
		{#if build}
			<BuildResult model={build} />
		{/if}
	{:else if id === 'activity'}
		<Activity store={activity} />
	{:else if id === 'tasks'}
		{#if tasks}
			<Tasks model={tasks} />
		{/if}
	{:else if id === 'devices'}
		{#if devices && emulators && install}
			<Devices {devices} {emulators} {install} connected={connection === 'open'} />
		{/if}
	{:else if id === 'tree'}
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
		{/if}
	{:else if id === 'act'}
		{#if act}
			<Act model={act} connected={connection === 'open'} />
		{/if}
	{/if}
{/snippet}

{#snippet banners()}
	{#if workspace && trust}
		<Banners {workspace} {trust} />
	{/if}
{/snippet}

<Shell content={panel} {status} {banners} {rail} />

<style>
	.empty {
		color: var(--text-3);
	}
</style>
