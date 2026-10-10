<script lang="ts">
	import { Client, type ConnectionState } from './lib/api/client';
	import { ActivityStore } from './lib/ops/activity';
	import Activity from './lib/ops/Activity.svelte';
	import { EventsHub } from './lib/build/hub';
	import type { Failure } from './lib/build/failure';
	import type { Result } from './lib/api/capabilities.gen';
	import { bootstrapToken, forgetToken } from './lib/api/token';
	import { browserRouteEnv, Router, type Route } from './lib/start/route';
	import { PickerModel } from './lib/start/picker';
	import Picker from './lib/start/Picker.svelte';
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

	// The view follows the address (ADR-0045 §2): `#w=<id>` is a project, no `w` the picker.
	const router = new Router(browserRouteEnv(window));
	let route = $state.raw<Route>(router.route);
	$effect(() => router.subscribe(() => (route = router.route)));

	let connection = $state<ConnectionState>('connecting');
	let health = $state.raw<Result<'system.health'> | null>(null);
	let healthError = $state<string | null>(null);

	let client = $state.raw<Client | null>(null);
	let activity = $state.raw<ActivityStore | null>(null);
	let hub = $state.raw<EventsHub | null>(null);
	let picker = $state.raw<PickerModel | null>(null);

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
		const projects = new PickerModel(
			{
				call: (name, params, opts) => c.call(name, params, opts),
				subscribe: (name, params, handlers) => c.subscribe(name, params, handlers),
			},
			(id) => router.go({ view: 'project', workspace: id }),
		);
		const c = new Client({
			url: `ws://${window.location.host}/v1/ws`,
			token,
			createSocket: (url) => new WebSocket(url),
			onState: (s) => {
				connection = s;
				store.onConnection(s);
				events.onConnection(s);
				projects.onConnection(s);
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
		picker = projects;
		// Connect first: a call made on a client never connected is refused as closed,
		// while one made while connecting waits for the socket.
		c.connect();
		store.start();
		events.start();
		return () => {
			projects.stop();
			events.stop();
			store.stop();
			c.close();
			client = null;
			activity = null;
			hub = null;
			picker = null;
		};
	});

	// The daemon refused the project the address names: the picker, with the reason.
	function missing(why: Failure) {
		picker?.showError(why);
		router.go({ view: 'picker' });
	}
</script>

{#snippet status()}
	<Status {connection} {health} {healthError} />
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
{:else if route.view === 'picker'}
	<!-- The picker stays a page of its own until the projects panel (plan m2d step 7). -->
	<main class="solo">
		<h1>Droidyard {@render status()}</h1>
		{#if picker}
			<Picker model={picker} />
		{/if}
		{#if activity}
			<Activity store={activity} />
		{/if}
	</main>
{:else if client && activity && hub}
	{#key route.workspace}
		<Project
			{client}
			{activity}
			{hub}
			{connection}
			workspaceId={route.workspace}
			onmissing={missing}
			onpicker={() => router.go({ view: 'picker' })}
			{status}
		/>
	{/key}
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
