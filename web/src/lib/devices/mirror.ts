// The devices mirror (plan m2b D11): *booting* is a non-terminal `emulator.start` or
// `emulator.wipe` (one boot key, so one at a time), *stopping* a non-terminal
// `emulator.stop`, *installing* a non-terminal `device.install`, whoever started it. It is
// read only from the activity store. The controls show the state and client kind and
// offer Cancel through `operation.cancel` (any session may cancel any operation); the UI
// never calls `operation.wait` on an id it did not start (that would take an agent's
// result from it).
import type { Params, Result } from '../api/capabilities.gen';
import type { CallOptions } from '../api/client';
import type { Operation, OperationTarget } from '../ops/activity';
import { failure, failureText } from '../build/failure';
import { inert } from './text';

export const BOOT_CAPABILITIES = ['emulator.start', 'emulator.wipe'] as const;
export const STOP_CAPABILITIES = ['emulator.stop'] as const;
export const INSTALL_CAPABILITIES = ['device.install'] as const;

/**
 * D13 (and plan §Changes): every terminal operation, by any client, that changes the
 * emulators or devices shown. `emulator.list` and `device.list` are never in it, so a
 * read is not its own trigger.
 */
export const DEVICE_TRIGGERS = [
	'emulator.start',
	'emulator.stop',
	'emulator.wipe',
	'device.install',
	'device.uninstall',
	'device.launch',
] as const;

export interface DevicesActivity {
	active(predicate: (op: Operation) => boolean): Operation[];
	subscribe(listener: () => void): () => void;
	onTerminal(capabilities: readonly string[], fn: (op: Operation) => void): () => void;
}

export interface DevicesMirrorClient {
	call(name: 'operation.cancel', params: Params<'operation.cancel'>, opts?: CallOptions): Promise<Result<'operation.cancel'>>;
}

export interface ActiveOp {
	operationId: string;
	capability: string;
	/** cli, mcp or ui. */
	client: string | undefined;
	state: 'queued' | 'running';
	/** What the operation acts on (ADR-0076): the device being stopped, the AVD being booted. */
	target: OperationTarget | undefined;
}

/**
 * What an operation shown acts on, as a sentence part (ADR-0076): " emulator-5554" for the
 * device being stopped, " Pixel_8" for the AVD being booted; "" when the operation names
 * none. The values are the caller's parameters; `inert` still shows control characters.
 */
export function targetText(op: Pick<ActiveOp, 'target'>): string {
	const name = op.target?.device || op.target?.avd;
	return name ? ` ${inert(name)}` : '';
}

export interface DevicesMirrorView {
	booting: ActiveOp | null;
	stopping: ActiveOp | null;
	installing: ActiveOp | null;
	/** The last Cancel that failed, as text. */
	cancelError: string | null;
}

export class DevicesMirror {
	#activity: DevicesActivity;
	#client: DevicesMirrorClient;
	#listeners = new Set<() => void>();
	#view: DevicesMirrorView | null = null;
	#cancelError: string | null = null;
	#off: () => void;

	constructor(activity: DevicesActivity, client: DevicesMirrorClient) {
		this.#activity = activity;
		this.#client = client;
		this.#off = activity.subscribe(() => this.#changed());
	}

	get view(): DevicesMirrorView {
		this.#view ??= {
			booting: this.#find(BOOT_CAPABILITIES),
			stopping: this.#find(STOP_CAPABILITIES),
			installing: this.#find(INSTALL_CAPABILITIES),
			cancelError: this.#cancelError,
		};
		return this.#view;
	}

	subscribe(listener: () => void): () => void {
		this.#listeners.add(listener);
		return () => {
			this.#listeners.delete(listener);
		};
	}

	/** Cancels the operation shown; a no-op for an id that is not. */
	async cancel(operationId: string): Promise<void> {
		const v = this.view;
		if (![v.booting, v.stopping, v.installing].some((o) => o?.operationId === operationId)) return;
		this.#cancelError = null;
		try {
			await this.#client.call('operation.cancel', { operationId });
		} catch (e) {
			this.#cancelError = failureText(failure(e));
		}
		this.#changed();
	}

	dispose(): void {
		this.#off();
	}

	#find(capabilities: readonly string[]): ActiveOp | null {
		const op = this.#activity
			.active((o) => capabilities.includes(o.capability))
			// Newest first from the store; the one running, if any, is the one to show.
			.sort((a, b) => (a.state === b.state ? 0 : a.state === 'running' ? -1 : 1))[0];
		if (op === undefined || (op.state !== 'queued' && op.state !== 'running')) return null;
		return { operationId: op.id, capability: op.capability, client: op.client, state: op.state, target: op.target };
	}

	#changed(): void {
		this.#view = null;
		for (const l of [...this.#listeners]) l();
	}
}
