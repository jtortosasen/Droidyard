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
	import GradleRootSelect from '../toolbar/GradleRootSelect.svelte';
	import DeviceMenu from '../toolbar/DeviceMenu.svelte';
	import TargetMenu from '../toolbar/TargetMenu.svelte';
	import RunButton from '../toolbar/RunButton.svelte';
	import { RunModel } from '../toolbar/run';
	import BuildStatus from '../toolbar/BuildStatus.svelte';
	import type { SessionsModel } from '../projects/sessions';
	import { sessionsByRoot, type ProjectsModel } from '../projects/projects';
	import { buildCommands, FileIndex, PaletteModel } from '../palette/palette';
	import Palette from '../palette/Palette.svelte';
	import type { ShellApi } from '../ui/panels';
	import LiveView from '../screen/LiveView.svelte';
	import type { InputClient } from '../screen/input';
	import { LiveViewModel } from '../screen/liveview';
	import { browserCodec } from '../screen/decoder';
	import { UiEventsModel } from '../devices/uievents';
	import UiEvents from '../devices/UiEvents.svelte';
	import { ConsoleModel } from '../build/console';
	import Console from '../build/Console.svelte';
	import type { EventsHub } from '../build/hub';
	import type { Failure } from '../build/failure';
	import { WorkspaceModel } from '../build/workspace';
	import { CommandRecordsModel, type CommandsClient } from '../ops/commands';
	import { RestoreModel, type RestoreClient } from '../ops/restore';
	import { TrashModel, type TrashClient } from '../ops/trash';
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
	import { LendingModel } from '../devices/lending';
	import { UiTreeModel } from '../devices/uitree';
	import Tree from '../devices/Tree.svelte';
	import { ActModel } from '../devices/act';
	import Act from '../devices/Act.svelte';
	import { AppLogsModel } from '../devices/applogs';
	import AppLogs from '../devices/AppLogs.svelte';
	import { FileTreeModel } from '../files/tree';
	import { FileOps } from '../files/ops';
	import { FileWatch } from '../files/watch';
	import FileTree from '../files/FileTree.svelte';
	import { GitWatch } from '../git/watch';
	import { HeadBase } from '../git/base';
	import { CommitText, DiffSource } from '../git/diffsource';
	import GitTree from '../git/GitTree.svelte';
	import { GitTreeModel } from '../git/tree';
	import DiffPanel from '../git/DiffPanel.svelte';
	import HistoryPanel from '../git/HistoryPanel.svelte';
	import { BranchModel } from '../git/branch';
	import { FetchModel } from '../git/fetch';
	import { PullModel } from '../git/pull';
	import { CompareModel } from '../git/compare';
	import { HistoryStore } from '../git/history';
	import { SubmodulesModel, initialisedRoots } from '../git/submodules';
	import { SubmoduleUpdateModel } from '../git/subupdate';
	import { TabsModel } from '../editor/tabs';
	import { ViewerSet } from '../editor/viewer';
	import type { UnsavedGuard } from '../editor/unsaved';
	import Editor from '../editor/Editor.svelte';
	import type { EditorDiagnostic } from '../editor/surface';
	import { followProblems, type Position } from '../build/locate';
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
		/** The session's token, which the screen socket presents. */
		token: string;
		/** Asks before unsaved text is lost; the open files attach to it. */
		guard: UnsavedGuard;
		/** The daemon refused the id: the page shows the projects panel with the reason. */
		onmissing: (why: Failure) => void;
		/** The connection and health line App owns, shown in the status bar. */
		status: Snippet;
		/** The sessions toggle, which App owns (it outlives the project view). */
		sessionsToggle: Snippet<[{ open: boolean; toggle: () => void; attract: number; register: (el: HTMLElement | null) => void }]>;
		/** The sessions column's body (the projects and agents panel), which App owns. */
		sessionsPanel: Snippet;
	}
	let { client, activity, sessions, projects, hub, connection, workspaceId, token, guard, onmissing, status, sessionsToggle, sessionsPanel }: Props = $props();

	// The sessions at each worktree root, for the Git panel's WORKTREES rows (ADR-0083 §4).
	let worktreeSessions = $state.raw<ReadonlyMap<string, readonly string[]>>(new Map());
	$effect(() => {
		const read = () => (worktreeSessions = sessionsByRoot(projects.view.rows, sessions.view.rows));
		read();
		const offP = projects.subscribe(read);
		const offS = sessions.subscribe(read);
		return () => {
			offP();
			offS();
		};
	});
	let workspace = $state.raw<WorkspaceModel | null>(null);
	// Restore of a delete any client made (plan m2j step 10): the workspace root is read at click.
	const restore = $derived(new RestoreModel({ call: ((n, p, o) => client.call(n, p, o)) as RestoreClient['call'] }, () => workspace?.root ?? ''));
	// The trash lists what was deleted before this tab (ADR-0096); Activity reads it.
	const trash = $derived(new TrashModel({ call: ((n, p, o) => client.call(n, p, o)) as TrashClient['call'] }, () => workspace?.root ?? ''));
	const records = $derived(new CommandRecordsModel({ call: ((n, p) => client.call(n, p)) as CommandsClient['call'] }));
	let trust = $state.raw<TrustModel | null>(null);
	let projectFile = $state.raw<ProjectFileModel | null>(null);
	let build = $state.raw<BuildModel | null>(null);
	let tasks = $state.raw<TasksModel | null>(null);
	let buildConsole = $state.raw<ConsoleModel | null>(null);
	let devices = $state.raw<DevicesModel | null>(null);
	let lending = $state.raw<LendingModel | null>(null);
	let emulators = $state.raw<EmulatorsModel | null>(null);
	let install = $state.raw<InstallModel | null>(null);
	let uiTree = $state.raw<UiTreeModel | null>(null);
	let act = $state.raw<ActModel | null>(null);
	let appLogs = $state.raw<AppLogsModel | null>(null);
	let liveView = $state.raw<LiveViewModel | null>(null);
	let uiEvents = $state.raw<UiEventsModel | null>(null);
	let fileTree = $state.raw<FileTreeModel | null>(null);
	let fileWatch = $state.raw<FileWatch | null>(null);
	let fileOps = $state.raw<FileOps | null>(null);
	// The Git status the Git tree and the Diff tab read (plan m2l step 5), and HEAD's text of the file the Diff tab shows.
	let gitWatch = $state.raw<GitWatch | null>(null);
	let gitTree = $state.raw<GitTreeModel | null>(null);
	let headBase = $state.raw<HeadBase | null>(null);
	// Which source the Diff tab shows (the working tree, or a changed file of a commit) and the commit file's two texts.
	let diffSource = $state.raw<DiffSource | null>(null);
	let commitText = $state.raw<CommitText | null>(null);
	let gitHistory = $state.raw<HistoryStore | null>(null);
	let gitFetch = $state.raw<FetchModel | null>(null);
	let gitPull = $state.raw<PullModel | null>(null);
	let gitBranches = $state.raw<BranchModel | null>(null);
	let gitCompare = $state.raw<CompareModel | null>(null);
	let gitSubmodules = $state.raw<SubmodulesModel | null>(null);
	let gitUpdater = $state.raw<SubmoduleUpdateModel | null>(null);
	let editorTabs = $state.raw<TabsModel | null>(null);
	let viewers = $state.raw<ViewerSet | null>(null);
	// D10: the last build's errors by file (the one copy: derived from the build model's result) and the
	// position a Problems row asked for, until the editor has moved there.
	let marks = $state.raw<ReadonlyMap<string, EditorDiagnostic[]>>(new Map());
	let target = $state.raw<(Position & { id: number }) | null>(null);
	let targetId = 0;
	let run = $state.raw<RunModel | null>(null);
	let palette = $state.raw<PaletteModel | null>(null);
	let treeView = $state<{ focus(): void } | undefined>();
	// Logcat follows the devices model's selection unless a device is picked there (D40);
	// "mine" is the application ids this tab saw built or installed (D39).
	let logcatDevice = $state.raw<LogcatDevice | null>(null);
	let mine = $state.raw<MineSet | null>(null);

	// A Problems row (D10): the file opens as a tree click opens it (a preview tab), the editor
	// moves to line:column, and the tree reveals it.
	function showProblem(at: Position) {
		if (!editorTabs) return;
		target = { ...at, id: ++targetId };
		diffSource?.showWorktree();
		editorTabs.open(at.path);
		void fileTree?.reveal(at.path);
	}

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
		const lent = new LendingModel(callOn);
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
		const runner = new RunModel(callOn, deviceList, builds, ws, installer);
		// ADR-0046: the project file preselects once it is read and is written from the
		// user's choices; nothing is written on load.
		const file = new ProjectFileModel(callOn, ws);
		const unbind = bindProjectFile({ file, build: builds, devices: deviceList, emulators: emulatorList, install: installer });
		const tree = new UiTreeModel(callOn, deviceList, store);
		const actions = new ActModel(callOn, deviceList);
		const logs = new AppLogsModel(callOn, deviceList, installer, store);
		const screen = new LiveViewModel(
			{
				subscribe: (name, params, handlers) => c.subscribe(name, params, handlers),
				call: (name, params) => c.call(name, params),
			},
			deviceList,
			// A remote device whose host stopped lending it ends its stream: the list is read again.
			{ supported: browserCodec() !== null, onDeviceGone: (serial) => deviceList.deviceGone(serial) },
		);
		// The screen's events of the toolbar's device, while the panel is shown (ADR-0112).
		const events = new UiEventsModel(
			{
				subscribe: (name, params, handlers) => c.subscribe(name, params, handlers),
				call: (name, params) => c.call(name, params),
			},
			deviceList,
		);
		const files = new FileTreeModel(callOn, ws);
		const watcher = new FileWatch(
			{
				call: callOn.call,
				subscribe: (name, params, handlers, opts) => c.subscribe(name, params, handlers, opts),
			},
			ws,
			files.sink,
		);
		// One git.watch for the Git tree and the Diff tab, beside the file watch.
		const gitStatus = new GitWatch(
			{
				call: callOn.call,
				subscribe: (name, params, handlers, opts) => c.subscribe(name, params, handlers, opts),
			},
			ws,
		);
		const headText = new HeadBase(callOn, ws);
		const source = new DiffSource();
		const commitTexts = new CommitText(callOn, ws);
		// The History tab's commits: read once the watch has a status, and again when HEAD moves.
		const history = new HistoryStore(callOn, ws, gitStatus);
		// Fetch (plan m2n P2): the refs are read again when it succeeds, and the activity feed says whether
		// the daemon holds it behind another write on the repository.
		const fetcher = new FetchModel(
			callOn,
			ws,
			() => history.refreshRefs(),
			{
				stateOf: (id) => store.operations.find((o) => o.id === id)?.state,
				subscribe: (listener) => store.subscribe(listener),
			},
		);
		// The viewer reads the same watch the tree does (no second file.watch).
		const tabs = new TabsModel(browserStorage(window), workspaceId);
		const readers = new ViewerSet(callOn, ws, { dirty: (path, dirty) => tabs.setDirty(path, dirty) });
		const offListen = watcher.listen(readers);
		const offGuard = guard.attach(readers);
		// Checkout and Create Branch (plan m2n P3): the editor's unsaved files stop a checkout before the daemon is
		// called, and the refs are read again when a call has ended. A clean buffer whose file the checkout rewrote
		// reloads through the file watch above (m2i D8).
		const comparer = new CompareModel(callOn, ws);
		const brancher = new BranchModel(callOn, ws, readers, () => history.refreshRefs());
		// Pull (plan m2n R3) is gated on the same unsaved files and reads the refs again when it succeeds; the log follows HEAD.
		const puller = new PullModel(callOn, ws, readers, () => history.refreshRefs(), {
			stateOf: (id) => store.operations.find((o) => o.id === id)?.state,
			subscribe: (listener) => store.subscribe(listener),
		});
		// The Submodules section (plan m2n P4) is read whenever the History store has a new answer of the refs.
		const submodules = new SubmodulesModel(callOn, ws);
		const offSubmodules = submodules.follow(history);
		// The log mixes the workspace with its initialised submodules (plan m2n P7).
		const offRoots = submodules.subscribe(() => history.setRoots(initialisedRoots(submodules.view.list)));
		// The section's Update (plan m2n P5): an update is a checkout in each submodule, so the editor's unsaved files
		// under them stop it, and the submodules are read again when it has ended, however it ended.
		// The summary (plan m2n R1) compares the list before and after the run, so the read is awaited; the refs are read again
		// too, for the branch each row shows.
		const submoduleUpdater = new SubmoduleUpdateModel(
			callOn,
			ws,
			readers,
			async () => {
				await submodules.load();
				history.refreshRefs();
			},
			{
				stateOf: (id) => store.operations.find((o) => o.id === id)?.state,
				subscribe: (listener) => store.subscribe(listener),
			},
			() => submodules.view.list,
		);
		// Another active file in the Editor is the working tree's diff again (a file opened from the tree or the palette changes it too).
		let activeFile = tabs.view.active;
		const offActive = tabs.subscribe(() => {
			const now = tabs.view.active;
			if (now === activeFile) return;
			activeFile = now;
			source.showWorktree();
		});
		// The palette's file index reads the same stream (no second file.watch).
		const index = new FileIndex();
		const offIndex = watcher.listen(index);
		// The Git tree's Tree view reads it too, with an expansion of its own.
		const treeOfGit = new GitTreeModel();
		const offGitTree = watcher.listen(treeOfGit);
		const logcatDevices = new LogcatDevice(deviceList);
		const mineIds = new MineSet(builds, installer);
		logcatDevice = logcatDevices;
		mine = mineIds;
		workspace = ws;
		trust = trusting;
		projectFile = file;
		build = builds;
		const offMarks = followProblems(builds, (m) => (marks = m));
		tasks = taskList;
		buildConsole = console_;
		devices = deviceList;
		lending = lent;
		emulators = emulatorList;
		install = installer;
		run = runner;
		uiTree = tree;
		act = actions;
		appLogs = logs;
		liveView = screen;
		uiEvents = events;
		fileTree = files;
		gitWatch = gitStatus;
		gitTree = treeOfGit;
		headBase = headText;
		diffSource = source;
		commitText = commitTexts;
		gitHistory = history;
		gitFetch = fetcher;
		gitPull = puller;
		gitBranches = brancher;
		gitCompare = comparer;
		gitSubmodules = submodules;
		gitUpdater = submoduleUpdater;
		// The models follow a rename or move before the tabs do (m2i D7), so their buffers are not dropped.
		const opsModel = new FileOps(callOn, ws, {
			moved: (from, to) => {
				readers.moved(from, to);
				tabs.moved(from, to);
			},
			unsaved: (path) => readers.holdsDirty(path),
			goneUnsaved: (dir) => readers.goneDirtyIn(dir),
		});
		fileOps = opsModel;
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
			// The root is known now: what was deleted before this tab is listed (ADR-0096).
			if (ok && !gone) void trash.load();
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
			offMarks();
			marks = new Map();
			target = null;
			mineIds.dispose();
			logcatDevices.dispose();
			offListen();
			offGuard();
			offActive();
			offIndex();
			offGitTree();
			readers.dispose();
			tabs.dispose();
			opsModel.reset();
			watcher.dispose();
			fetcher.dispose();
			puller.dispose();
			brancher.dispose();
			comparer.dispose();
			offSubmodules();
			offRoots();
			submodules.dispose();
			submoduleUpdater.dispose();
			history.dispose();
			gitStatus.dispose();
			headText.dispose();
			commitTexts.dispose();
			source.dispose();
			files.dispose();
			unbind();
			file.dispose();
			logs.dispose();
			actions.dispose();
			tree.dispose();
			runner.dispose();
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
			lending = null;
			emulators = null;
			install = null;
			run = null;
			uiTree = null;
			act = null;
			screen.dispose();
			liveView = null;
			events.dispose();
			uiEvents = null;
			appLogs = null;
			fileTree = null;
			fileOps = null;
			fileWatch = null;
			gitWatch = null;
			gitTree = null;
			headBase = null;
			diffSource = null;
			commitText = null;
			gitHistory = null;
			gitFetch = null;
			gitPull = null;
			gitBranches = null;
			gitCompare = null;
			gitSubmodules = null;
			gitUpdater = null;
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
		gitWatch?.onConnection(connection);
	});
	$effect(() => {
		if (connection !== 'open' || !devices || !emulators) return;
		void devices.refresh();
		void emulators.refresh();
		void lending?.refresh();
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
	{:else if id === 'devicemenu'}
		{#if devices && emulators}
			<DeviceMenu {devices} {emulators} />
		{/if}
	{:else if id === 'target'}
		{#if build}
			<TargetMenu model={build} />
		{/if}
	{:else if id === 'run'}
		{#if run}
			<RunButton model={run} />
		{/if}
	{:else if id === 'files'}
		{#if fileTree && fileWatch && fileOps}
			<FileTree
				bind:this={treeView}
				model={fileTree}
				watch={fileWatch}
				ops={fileOps}
				onopen={(path) => {
					target = null;
					diffSource?.showWorktree();
					editorTabs?.open(path);
				}}
				{workspaceId}
			/>
		{/if}
	{:else if id === 'git-side'}
		{#if gitWatch && gitTree}
			<GitTree
				watch={gitWatch}
				tree={gitTree}
				{workspaceId}
				onopen={(path) => {
					// A changed file opens as the Editor's preview tab, so it has one buffer, and its diff shows.
					target = null;
					diffSource?.showWorktree();
					editorTabs?.open(path);
					shell.show('diff');
				}}
			/>
		{/if}
	{:else if id === 'git'}
		{#if gitHistory && gitWatch && gitFetch && gitBranches && gitCompare && gitSubmodules && gitUpdater}
			<HistoryPanel
				store={gitHistory}
				fetcher={gitFetch}
				puller={gitPull ?? undefined}
				branches={gitBranches}
				compare={gitCompare}
				submodules={gitSubmodules}
				updater={gitUpdater}
				{worktreeSessions}
				watch={gitWatch}
				{workspaceId}
				onopenfile={(file) => {
					// A changed file of the opened commit (or between two tips): the Diff tab shows it against the commit's first parent (or the base).
					diffSource?.showCommit(file);
					shell.show('diff');
				}}
			/>
		{/if}
	{:else if id === 'diff'}
		{#if editorTabs && viewers && gitWatch && headBase && diffSource && commitText}
			<DiffPanel tabs={editorTabs} {viewers} git={gitWatch} base={headBase} source={diffSource} {commitText} />
		{/if}
	{:else if id === 'editor'}
		{#if editorTabs && viewers && fileTree}
			{@const files = fileTree}
			<Editor
				tabs={editorTabs}
				{viewers}
				{guard}
				{marks}
				{target}
				ontarget={(id) => {
					if (target?.id === id) target = null;
				}}
				onactivate={(path) => void files.reveal(path)}
				copyPath={(path) => files.absolutePath(path)}
			/>
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
			<BuildResult model={build} onlocate={(at) => {
					shell.show('editor');
					showProblem(at);
				}} />
		{/if}
	{:else if id === 'activity'}
		{#if buildConsole && build}
			<Activity store={activity} {sessions} console={buildConsole} {build} {restore} {trash} {records} {workspaceId} onopenoutput={() => shell.show('console')} />
		{/if}
	{:else if id === 'gradleroot'}
		{#if workspace && projectFile}
			<GradleRootSelect {workspace} file={projectFile} />
		{/if}
	{:else if id === 'tasks'}
		{#if tasks}
			<TaskSelect model={tasks} />
		{/if}
	{:else if id === 'devices'}
		{#if devices && emulators && install && lending}
			<Devices {devices} {emulators} {install} {sessions} {lending} connected={connection === 'open'} />
		{/if}
	{:else if id === 'liveview'}
		{#if liveView}
			<LiveView model={liveView} {token} {connection} visible={shell.rightOpen()} input={{ call: ((n, p) => client.call(n, p)) as InputClient['call'] }} />
		{/if}
	{:else if id === 'uievents'}
		{#if uiEvents}
			<UiEvents model={uiEvents} {connection} visible={shell.rightOpen()} />
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
	{#if palette && workspace && build && tasks && emulators && fileTree && editorTabs && run}
		{@const ws = workspace}
		{@const builds = build}
		{@const taskList = tasks}
		{@const emus = emulators}
		{@const files = fileTree}
		{@const tabs = editorTabs}
		{@const runner = run}
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
					run: runner,
					openSessions: shell.openSessions,
					density: { get: shell.density, set: shell.setDensity },
					focusTree: () => {
						shell.showFiles();
						void tick().then(() => treeView?.focus());
					},
				})}
			onopen={(path) => {
				shell.show('editor');
				target = null;
				diffSource?.showWorktree();
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

