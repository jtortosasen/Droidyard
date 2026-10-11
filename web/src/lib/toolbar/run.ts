// The toolbar's Run (m2e D37): ▶ starts a pending AVD, assembles the chosen module and
// variant, and only on a successful build installs that build's own output (`buildId`,
// ADR-0023 §5) and launches its application id. The install and launch go through
// `InstallModel`, so the project file, the right panel and "mine" hear of them as of a
// click there. A build's `failed` and `cancelled` are results, not errors (ADR-0020): no
// install follows either. ■ cancels only the operation the current step started, by the
// id `CallOptions.onOperation` gave; pressed before the id is known, the cancel is sent
// when it arrives. Each step is an ordinary operation, so Activity shows it.
import type { Params, Result } from '../api/capabilities.gen';
import type { CallOptions } from '../api/client';
import { failure, type Failure } from '../build/failure';
import type { ActiveOp } from '../devices/mirror';

type Calls = 'emulator.start' | 'build.assemble' | 'operation.cancel';

export interface RunClient {
	call<N extends Calls>(name: N, params: Params<N>, opts?: CallOptions): Promise<Result<N>>;
}

export interface RunDevices {
	view: { selected: string; pendingAvd: string };
	select(serial: string): void;
	subscribe(listener: () => void): () => void;
}

export interface RunBuild {
	view: { module: string; variant: string };
	subscribe(listener: () => void): () => void;
}

export interface RunWorkspace {
	readonly root: string;
	readonly trusted?: boolean;
	subscribe(listener: () => void): () => void;
}

export interface RunInstall {
	mirror: { view: { installing: ActiveOp | null } };
	view: { installError: Failure | null; launchError: Failure | null };
	install(x: {
		device: string;
		build: { buildId: string; module: string; variant: string };
		onOperation?: (operationId: string) => void;
	}): Promise<Result<'device.install'> | null>;
	launch(x: { device: string; applicationId: string }): Promise<Result<'device.launch'> | null>;
}

export type RunStep = 'start' | 'build' | 'install' | 'launch';

export type RunState =
	| 'idle'
	| 'starting'
	| 'building'
	| 'installing'
	| 'launching'
	| 'done'
	| 'failed'
	| 'build-failed'
	| 'cancelled';

export interface RunView {
	state: RunState;
	/** In progress: ■ replaces ▶. */
	running: boolean;
	/** ■ cancels now: a launch is short and is not cancelled, so ■ is disabled during it. */
	cancellable: boolean;
	/** The step an operation error stopped the run at, with what the daemon said. */
	failed: { step: RunStep; failure: Failure } | null;
	/** Why ▶ does nothing, or null. */
	disabled: string | null;
}

const RUNNING: readonly RunState[] = ['starting', 'building', 'installing', 'launching'];
const CANCELLABLE: readonly RunState[] = ['starting', 'building', 'installing'];

export class RunModel {
	#client: RunClient;
	#devices: RunDevices;
	#build: RunBuild;
	#ws: RunWorkspace;
	#install: RunInstall;
	#listeners = new Set<() => void>();
	#offs: (() => void)[] = [];
	#view: RunView | null = null;
	#state: RunState = 'idle';
	#failed: RunView['failed'] = null;
	#disposed = false;
	#cancelRequested = false;
	// The operation of the step in flight: null until its first answer gives the id.
	#opId: string | null = null;

	constructor(client: RunClient, devices: RunDevices, build: RunBuild, ws: RunWorkspace, install: RunInstall) {
		this.#client = client;
		this.#devices = devices;
		this.#build = build;
		this.#ws = ws;
		this.#install = install;
		const changed = () => this.#changed();
		this.#offs.push(devices.subscribe(changed), build.subscribe(changed), ws.subscribe(changed));
	}

	get view(): RunView {
		this.#view ??= {
			state: this.#state,
			running: RUNNING.includes(this.#state),
			cancellable: CANCELLABLE.includes(this.#state),
			failed: this.#failed,
			disabled: this.#disabledReason(),
		};
		return this.#view;
	}

	subscribe(listener: () => void): () => void {
		this.#listeners.add(listener);
		return () => {
			this.#listeners.delete(listener);
		};
	}

	#disabledReason(): string | null {
		if (RUNNING.includes(this.#state)) return 'A run is in progress';
		if (this.#ws.trusted !== true) return 'The workspace is not trusted';
		if (this.#build.view.module === '' || this.#build.view.variant === '') return 'No module and variant chosen';
		if (this.#devices.view.selected === '' && this.#devices.view.pendingAvd === '') return 'No device or AVD chosen';
		return null;
	}

	/** ▶. Ignored while a run is in progress or when disabled. */
	async run(): Promise<void> {
		if (this.#disposed || this.view.disabled !== null) return;
		const { module, variant } = this.#build.view;
		const root = this.#ws.root.trim();
		const avd = this.#devices.view.pendingAvd;
		let device = this.#devices.view.selected;
		this.#cancelRequested = false;
		this.#failed = null;

		const stopped = () => this.#disposed || this.#cancelRequested;
		const end = (state: RunState, failed: RunView['failed'] = null) => {
			if (this.#disposed) return;
			this.#opId = null;
			this.#failed = failed;
			this.#set(state);
		};
		const ended = () => {
			if (this.#disposed) return;
			end(this.#cancelRequested ? 'cancelled' : 'idle');
		};
		const fail = (step: RunStep, f: Failure) => end('failed', { step, failure: f });
		const onOperation = (id: string) => {
			this.#opId = id;
			if (this.#cancelRequested) void this.#sendCancel(id);
		};

		if (avd !== '') {
			this.#opId = null;
			this.#set('starting');
			try {
				const r = await this.#client.call('emulator.start', { avd }, { onOperation });
				device = r.device;
				// The device is the toolbar's choice from now on (D36).
				if (!this.#disposed) this.#devices.select(device);
			} catch (e) {
				if (this.#cancelRequested) return ended();
				return fail('start', failure(e));
			}
			if (stopped()) return ended();
		}

		this.#opId = null;
		this.#set('building');
		let build: Result<'build.assemble'>;
		try {
			build = await this.#client.call('build.assemble', { module, variant }, { workspace: root, onOperation });
		} catch (e) {
			if (this.#cancelRequested) return ended();
			return fail('build', failure(e));
		}
		if (stopped()) return ended();
		if (build.outcome === 'cancelled') return end('cancelled');
		if (build.outcome === 'failed') return end('build-failed');

		if (this.#install.mirror.view.installing !== null) {
			return fail('install', { message: 'Another client is installing an app on a device; try again when it ends.' });
		}
		this.#opId = null;
		this.#set('installing');
		const installed = await this.#install.install({ device, build: { buildId: build.buildId, module, variant }, onOperation });
		if (this.#cancelRequested && installed === null) return ended();
		if (installed === null) {
			return fail('install', this.#install.view.installError ?? { message: 'The install did not start.' });
		}
		if (stopped()) return ended();

		this.#opId = null;
		this.#set('launching');
		const launched = await this.#install.launch({ device, applicationId: installed.applicationId });
		if (launched === null) {
			return fail('launch', this.#install.view.launchError ?? { message: 'The launch did not start.' });
		}
		end('done');
	}

	/** ■. Cancels the current step's own operation; no step follows. A launch is short and is not cancelled. */
	cancel(): void {
		if (this.#disposed || !CANCELLABLE.includes(this.#state)) return;
		this.#cancelRequested = true;
		if (this.#opId !== null) void this.#sendCancel(this.#opId);
	}

	async #sendCancel(operationId: string): Promise<void> {
		try {
			await this.#client.call('operation.cancel', { operationId });
		} catch {
			// The operation ended first, or the connection went; the step's own answer settles the run.
		}
	}

	/** Stops between steps: a step in flight finishes on its own, and nothing follows it. */
	dispose(): void {
		this.#disposed = true;
		for (const off of this.#offs) off();
		this.#offs = [];
		this.#listeners.clear();
	}

	#set(state: RunState): void {
		this.#state = state;
		this.#changed();
	}

	#changed(): void {
		this.#view = null;
		for (const l of [...this.#listeners]) l();
	}
}
