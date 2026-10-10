<script lang="ts">
	import { Client, type ConnectionState } from './lib/api/client';
	import { ActivityStore } from './lib/ops/activity';
	import Activity from './lib/ops/Activity.svelte';
	import Logcat from './lib/logcat/Logcat.svelte';
	import { BuildModel } from './lib/build/build';
	import Build from './lib/build/Build.svelte';
	import { EventsHub } from './lib/build/hub';
	import { WorkspaceModel } from './lib/build/workspace';
	import Workspace from './lib/build/Workspace.svelte';
	import type { Result } from './lib/api/capabilities.gen';
	import { bootstrapToken, forgetToken } from './lib/api/token';

	// The token comes from the fragment `droidyard ui` opened, once; a tab without one,
	// or whose token the daemon refused, shows the instruction (http-websocket.md
	// §Security baseline).
	const token = bootstrapToken({
		location: window.location,
		history: window.history,
		storage: window.sessionStorage,
	});

	let connection = $state<ConnectionState>('connecting');
	let health = $state.raw<Result<'system.health'> | null>(null);
	let healthError = $state<string | null>(null);

	let client = $state.raw<Client | null>(null);
	let activity = $state.raw<ActivityStore | null>(null);
	let workspace = $state.raw<WorkspaceModel | null>(null);
	let build = $state.raw<BuildModel | null>(null);
	let devices = $state.raw<Result<'device.list'>['devices']>([]);
	let devicesError = $state<string | null>(null);
	let device = $state('');

	$effect(() => {
		if (token === null) return;
		// Created before the client so the connection's state reaches it from the first change.
		const store = new ActivityStore({
			subscribe: (name, params, handlers) => c.subscribe(name, params, handlers),
			call: (name, params) => c.call(name, params),
		});
		const hub = new EventsHub({
			subscribe: (name, params, handlers) => c.subscribe(name, params, handlers),
			call: (name, params) => c.call(name, params),
		});
		const callOn = {
			call: ((name, params, opts) => c.call(name, params, opts)) as Client['call'],
		};
		// The typed root is a view preference; blocked storage leaves it to be typed again.
		let storage: Storage | null = null;
		try {
			storage = window.localStorage;
		} catch {
			// Blocked storage.
		}
		const ws = new WorkspaceModel(callOn, hub, storage);
		const builds = new BuildModel(callOn, store, hub, ws);
		const c = new Client({
			url: `ws://${window.location.host}/v1/ws`,
			token,
			createSocket: (url) => new WebSocket(url),
			onState: (s) => {
				connection = s;
				store.onConnection(s);
				hub.onConnection(s);
				if (s === 'unauthorized') forgetToken(window.sessionStorage);
				if (s === 'open') refresh();
			},
		});
		function refresh() {
			listDevices(c);
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
		workspace = ws;
		build = builds;
		// Connect first: a call made on a client never connected is refused as closed,
		// while one made while connecting waits for the socket.
		c.connect();
		store.start();
		hub.start();
		// A root kept from an earlier visit is opened once (workspace.open is idempotent);
		// after another client closes it the panel offers to reopen, it does not reopen.
		void ws.open();
		return () => {
			builds.dispose();
			ws.dispose();
			hub.stop();
			store.stop();
			c.close();
			client = null;
			activity = null;
			workspace = null;
			build = null;
		};
	});

	function listDevices(c: Client) {
		c.call('device.list', {}).then(
			(r) => {
				devices = r.devices;
				devicesError = null;
				// One device to choose from is the choice, until the user picks another.
				const online = r.devices.filter((d) => d.online);
				if (device === '' && online.length === 1) device = online[0]!.device;
			},
			(e: unknown) => {
				if (e instanceof Error && 'code' in e) devicesError = String(e.code);
			},
		);
	}

	function deviceLabel(d: Result<'device.list'>['devices'][number]): string {
		const name = d.avdName ?? d.model ?? d.product ?? d.kind;
		return `${d.device} (${name}, ${d.state})`;
	}

	function uptime(seconds: number): string {
		const h = Math.floor(seconds / 3600);
		const m = Math.floor((seconds % 3600) / 60);
		return h > 0 ? `${h} h ${m} min` : m > 0 ? `${m} min ${seconds % 60} s` : `${seconds} s`;
	}
</script>

<h1>Droidyard</h1>

{#if token === null}
	<p>No token in this tab: run <code>droidyard ui</code>.</p>
{:else if connection === 'unauthorized'}
	<p>The daemon restarted: run <code>droidyard ui</code>.</p>
{:else}
	<p>Connection: <strong>{connection}</strong></p>
	{#if health}
		<p>
			Daemon {health.version ?? 'unknown version'}, up
			{health.uptimeSeconds === undefined ? 'unknown' : uptime(health.uptimeSeconds)}
		</p>
	{/if}
	{#if healthError}
		<p>system.health failed: {healthError}</p>
	{/if}

	<p>
		<label>
			Device
			<select value={device} onchange={(e) => (device = e.currentTarget.value)}>
				<option value="">none</option>
				{#each devices as d (d.device)}
					<option value={d.device} disabled={!d.online}>{deviceLabel(d)}</option>
				{/each}
			</select>
		</label>
		<button type="button" disabled={!client || connection !== 'open'} onclick={() => client && listDevices(client)}>
			Refresh
		</button>
		{#if devicesError}
			device.list failed: {devicesError}
		{:else if devices.length === 0}
			No device is connected.
		{/if}
	</p>

	{#if workspace}
		<Workspace model={workspace} />
	{/if}
	{#if build}
		<Build model={build} />
	{/if}

	{#if client}
		<Logcat {client} {connection} {device} />
	{/if}

	{#if activity}
		<Activity store={activity} />
	{/if}
{/if}

<style>
	:global(body) {
		font-family: system-ui, sans-serif;
		margin: 1rem;
		line-height: 1.4;
	}
</style>
