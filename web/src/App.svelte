<script lang="ts">
	import { Client, type ConnectionState } from './lib/api/client';
	import { ActivityStore } from './lib/ops/activity';
	import { EventsHub } from './lib/build/hub';
	import type { Failure } from './lib/build/failure';
	import type { Result } from './lib/api/capabilities.gen';
	import type { ProjectsView } from './lib/projects/projects';
	import { bootstrapToken, forgetToken } from './lib/api/token';
	import { browserRouteEnv, Router, type Route } from './lib/start/route';
	import { landing, ProjectsModel } from './lib/projects/projects';
	import { SessionsModel } from './lib/projects/sessions';
	import Projects from './lib/projects/Projects.svelte';
	import Rail from './lib/projects/Rail.svelte';
	import Project from './lib/start/Project.svelte';
	import Status from './lib/ui/Status.svelte';

	// The token comes from the fragment `droidyard ui` opened, once; a tab without one,
	// or whose token the daemon refused, shows the instruction (http-websocket.md
	// §Security baseline). The fragment's `w` stays: it names the tab's project.
	const token = bootstrapToken({
		location: window.location,
		history: window.history,
		storage: window.sessionStorage,
	});

	// The view follows the address (ADR-0047): `#w=<id>` is a project; without `w` the page
	// shows the newest open project, else the projects panel.
	const router = new Router(browserRouteEnv(window));
	let route = $state.raw<Route>(router.route);
	$effect(() =>
		router.subscribe(() => {
			route = router.route;
			if (route.view === 'project') refused = false;
		}),
	);

	let connection = $state<ConnectionState>('connecting');
	let health = $state.raw<Result<'system.health'> | null>(null);
	let healthError = $state<string | null>(null);

	let client = $state.raw<Client | null>(null);
	let activity = $state.raw<ActivityStore | null>(null);
	let hub = $state.raw<EventsHub | null>(null);
	let projects = $state.raw<ProjectsModel | null>(null);
	let sessions = $state.raw<SessionsModel | null>(null);
	let projectsView = $state.raw<ProjectsView | null>(null);
	// A `w` the daemon refused: the panel stays up with the reason until a project is chosen.
	let refused = $state(false);
	// The panel, pinned from "All projects".
	let pinned = $state(false);

	$effect(() => {
		if (token === null) return;
		// Created before the client so the connection's state reaches it from the first change.
		const store = new ActivityStore({
			subscribe: (name, params, handlers) => c.subscribe(name, params, handlers),
			call: (name, params) => c.call(name, params),
		});
		const events = new EventsHub({
			subscribe: (name, params, handlers) => c.subscribe(name, params, handlers),
			call: (name, params) => c.call(name, params),
		});
		const list = new ProjectsModel(
			{
				call: (name, params, opts) => c.call(name, params, opts),
				subscribe: (name, params, handlers) => c.subscribe(name, params, handlers),
			},
			(id) => {
				refused = false;
				router.go({ view: 'project', workspace: id });
			},
		);
		const agents = new SessionsModel(store);
		const c = new Client({
			url: `ws://${window.location.host}/v1/ws`,
			token,
			createSocket: (url) => new WebSocket(url),
			onState: (s) => {
				connection = s;
				store.onConnection(s);
				events.onConnection(s);
				list.onConnection(s);
				if (s === 'unauthorized') forgetToken(window.sessionStorage);
				if (s === 'open') refresh();
			},
		});
		function refresh() {
			c.call('system.health', {}).then(
				(h) => {
					health = h;
					healthError = null;
				},
				(e: unknown) => {
					// A lost connection is shown by the state; a capability error by its code.
					if (e instanceof Error && 'code' in e) healthError = String(e.code);
				},
			);
		}
		client = c;
		activity = store;
		hub = events;
		projects = list;
		sessions = agents;
		const offList = list.subscribe(() => (projectsView = list.view));
		projectsView = list.view;
		// Connect first: a call made on a client never connected is refused as closed,
		// while one made while connecting waits for the socket.
		c.connect();
		store.start();
		events.start();
		list.start();
		agents.start();
		return () => {
			offList();
			agents.stop();
			list.stop();
			events.stop();
			store.stop();
			c.close();
			client = null;
			activity = null;
			hub = null;
			projects = null;
			sessions = null;
			projectsView = null;
		};
	});

	// The daemon refused the project the address names: the projects panel, with the reason.
	// The refused entry is replaced, so Back does not return to it.
	function missing(why: Failure) {
		refused = true;
		projects?.showError(why);
		router.replace({ view: 'none' });
	}

	const lands = $derived(projectsView ? landing(route, projectsView, refused) : { kind: 'waiting' as const });
	// Without `w` the address takes the newest open project's id, so a reload stays on it.
	$effect(() => {
		if (route.view === 'none' && lands.kind === 'project') router.replace({ view: 'project', workspace: lands.workspace });
	});
</script>

{#snippet status()}
	<Status {connection} {health} {healthError} />
{/snippet}

{#snippet rail()}
	{#if projects && sessions}
		<Rail {projects} {sessions} selected={route.view === 'project' ? route.workspace : null} bind:pinned />
	{/if}
{/snippet}

{#if token === null}
	<main class="solo">
		<h1>Droidyard</h1>
		<p>No token in this tab: run <code>droidyard ui</code>.</p>
	</main>
{:else if connection === 'unauthorized'}
	<main class="solo">
		<h1>Droidyard</h1>
		<p>The daemon restarted: run <code>droidyard ui</code>.</p>
	</main>
{:else if client && activity && hub && projects && sessions}
	{#if route.view === 'project'}
		{#key route.workspace}
			<Project
				{client}
				{activity}
				{sessions}
				{hub}
				{connection}
				workspaceId={route.workspace}
				onmissing={missing}
				onprojects={() => (pinned = true)}
				{status}
				{rail}
			/>
		{/key}
	{:else if lands.kind === 'panel'}
		<!-- No project to show: the panel itself, pinned, with recents, "Open folder..." and any reason. -->
		<main class="solo">
			<h1>Droidyard {@render status()}</h1>
			<Projects {projects} {sessions} selected={null} />
		</main>
	{/if}
{/if}

<style>
	.solo {
		height: 100%;
		overflow: auto;
		padding: var(--sp-6);
		max-width: 960px;
		margin: 0 auto;
	}
	h1 {
		display: flex;
		align-items: center;
		gap: var(--sp-5);
		margin: 0 0 var(--sp-5);
		font-size: 18px;
	}
</style>
