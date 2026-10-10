<script lang="ts">
	import { CapabilityError, type Client, type ConnectionState } from '../api/client';
	import type { ActivityStore } from '../ops/activity';
	import Activity from '../ops/Activity.svelte';
	import Logcat from '../logcat/Logcat.svelte';
	import { LogcatDevice } from '../logcat/device';
	import { MineSet } from '../logcat/mine';
	import { BuildModel } from '../build/build';
	import Build from '../build/Build.svelte';
	import BuildResult from '../build/BuildResult.svelte';
	import { TasksModel } from '../build/tasks';
	import TaskSelect from '../toolbar/TaskSelect.svelte';
	import BuildStatus from '../toolbar/BuildStatus.svelte';
	import type { SessionsModel } from '../projects/sessions';
	import type { ProjectsModel } from '../projects/projects';
	import { buildCommands, FileIndex, PaletteModel } from '../palette/palette';
	import Palette from '../palette/Palette.svelte';
	import type { ShellApi } from '../ui/panels';
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
	import { TabsModel } from '../editor/tabs';
	import { ViewerSet } from '../editor/viewer';
	import Editor from '../editor/Editor.svelte';
	import { browserStorage } from '../ui/layout';
	import Banners from './Banners.svelte';
	import Shell from '../ui/Shell.svelte';
	import { tick, type Snippet } from 'svelte';

	// One project's panels (ADR-0047): the workspace the address's `w` names, over the
	// session App owns. App mounts this keyed on the id, so a different
	// project is a new instance, never a model re-pointed.
	interface Props {
		client: Client;
		activity: ActivityStore;
		/** The agents the projects panel lists; its selected row filters Activity (D30). */
		sessions: SessionsModel;
		/** The projects list the palette's "Switch project" reads. */
		projects: ProjectsModel;
		hub: EventsHub;
		connection: ConnectionState;
		workspaceId: string;
		/** The daemon refused the id: the page shows the projects panel with the reason. */
		onmissing: (why: Failure) => void;
		/** The connection and health line App owns, shown in the status bar. */
		status: Snippet;
		/** The sessions toggle, which App owns (it outlives the project view). */
		sessionsToggle: Snippet<[{ open: boolean; toggle: () => void }]>;
		/** The sessions column's body (the projects and agents panel), which App owns. */
		sessionsPanel: Snippet;
	}
	let { client, activity, sessions, projects, hub, connection, workspaceId, onmissing, status, sessionsToggle, sessionsPanel }: Props = $props();

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
	let editorTabs = $state.raw<TabsModel | null>(null);
	let viewers = $state.raw<ViewerSet | null>(null);
	let palette = $state.raw<PaletteModel | null>(null);
	let treeView = $state<{ focus(): void } | undefined>();
	// Logcat follows the devices model's selection unless a device is picked there (D40);
	// "mine" is the application ids this tab saw built or installed (D39).
	let logcatDevice = $state.raw<LogcatDevice | null>(null);
	let mine = $state.raw<MineSet | null>(null);

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
		// The viewer reads the same watch the tree does (no second file.watch).
		const tabs = new TabsModel(browserStorage(window), workspaceId);
		const readers = new ViewerSet(callOn, ws);
		const offListen = watcher.listen(readers);
		// The palette's file index reads the same stream (no second file.watch).
		const index = new FileIndex();
		const offIndex = watcher.listen(index);
		const logcatDevices = new LogcatDevice(deviceList);
		const mineIds = new MineSet(builds, installer);
		logcatDevice = logcatDevices;
		mine = mineIds;
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
		editorTabs = tabs;
		viewers = readers;
		palette = new PaletteModel(index, () => [], () => {});
		// The project is opened by the id from the address (workspace.open is idempotent);
		// after another client closes it the panel offers to reopen, it does not reopen.
		// A refusal by the daemon shows the projects panel with the reason; a lost connection does not.
		let gone = false;
		void ws.openById(workspaceId).then((ok) => {
			const why = ws.view.error;
			if (!ok && !gone && why?.code) onmissing(why);
			if (ok && !gone) {
				// A remembered tab whose file is gone is dropped; any other failure keeps it.
				void tabs.restore(async (path) => {
					try {
						await callOn.call('file.read', { path, limit: 4 }, { workspace: ws.root.trim() });
						return true;
					} catch (e) {
						return !(e instanceof CapabilityError && e.code === 'FILE_NOT_FOUND');
					}
				});
			}
		});
		return () => {
			gone = true;
			mineIds.dispose();
			logcatDevices.dispose();
			offListen();
			offIndex();
			readers.dispose();
			tabs.dispose();
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
			editorTabs = null;
			viewers = null;
			palette = null;
			logcatDevice = null;
			mine = null;
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

{#snippet panel(id: string, shell: ShellApi)}
	{#if id === 'workspace'}
		{#if workspace && trust}
			<Workspace model={workspace} {trust} onprojects={shell.openSessions} compact />
		{/if}
	{:else if id === 'build'}
		{#if build}
			<Build model={build} compact />
		{/if}
	{:else if id === 'files'}
		{#if fileTree && fileWatch}
			<FileTree bind:this={treeView} model={fileTree} watch={fileWatch} onopen={(path) => editorTabs?.open(path)} />
		{/if}
	{:else if id === 'editor'}
		{#if editorTabs && viewers && fileTree}
			{@const files = fileTree}
			<Editor tabs={editorTabs} {viewers} onactivate={(path) => void files.reveal(path)} copyPath={(path) => files.absolutePath(path)} />
		{/if}
	{:else if id === 'logcat'}
		{#if logcatDevice && mine}
			<Logcat {client} {connection} devices={logcatDevice} {mine} {workspaceId} />
		{/if}
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
		{#if buildConsole && build}
			<Activity store={activity} {sessions} console={buildConsole} {build} onopenoutput={() => shell.show('console')} />
		{/if}
	{:else if id === 'tasks'}
		{#if tasks}
			<TaskSelect model={tasks} />
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

{#snippet statusbar()}
	{@render status()}
	{#if projectFile}
		<span class="end"><ProjectFile model={projectFile} compact /></span>
	{/if}
{/snippet}

{#snippet banners()}
	{#if workspace && trust}
		<Banners {workspace} {trust} />
	{/if}
{/snippet}

{#snippet centre()}
	{#if build}
		<BuildStatus {build} {sessions} />
	{/if}
{/snippet}

{#snippet overlay(shell: ShellApi)}
	{#if palette && workspace && build && tasks && emulators && fileTree && editorTabs}
		{@const ws = workspace}
		{@const builds = build}
		{@const taskList = tasks}
		{@const emus = emulators}
		{@const files = fileTree}
		{@const tabs = editorTabs}
		<Palette
			model={palette}
			commands={() =>
				buildCommands({
					workspace: ws,
					build: builds,
					tasks: taskList,
					emulators: emus,
					projects,
					currentProject: workspaceId,
					tree: files,
					density: { get: shell.density, set: shell.setDensity },
					focusTree: () => {
						shell.showFiles();
						void tick().then(() => treeView?.focus());
					},
				})}
			onopen={(path) => {
				shell.show('editor');
				tabs.open(path);
			}}
		/>
	{/if}
{/snippet}

<Shell content={panel} {statusbar} {centre} {banners} {sessionsToggle} sessions={sessionsPanel} {overlay} />

<style>
	.end {
		margin-left: auto;
	}
</style>

