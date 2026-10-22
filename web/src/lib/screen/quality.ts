// The quality of a remote device's live view (plan m2za step 3; ADR-0118 §4, ADR-0123): the
// remote's `fast` mark, which `policy.get` lists in `remotes`, and the one write the UI makes of
// it, the Fast toggle. The remote is the selected device's `host` (`device.list`); a device of
// this machine has none and shows no toggle. The mark is read at each answered watch of a remote
// device and after this model's own write; the daemon applies a change within seconds and the
// open view restarts by itself (`renewed`).
//
// A Merge Patch replaces an array whole, so the write sends the whole list back with only the
// entry's mark changed (its addresses and fingerprint stay as read), with the revision it was
// read at. After a concurrent edit (POLICY_REVISION_CONFLICT) the policy is read again and the
// write decided again, once, as lending.ts does for Stop lending.
import type { Params, Result } from '../api/capabilities.gen';
import { CapabilityError, type CallOptions } from '../api/client';
import { failure, type Failure } from '../build/failure';

type Calls = 'policy.get' | 'policy.update';

export interface QualityClient {
	call<N extends Calls>(name: N, params: Params<N>, opts?: CallOptions): Promise<Result<N>>;
}

export interface QualityDevices {
	view: { selected: string; devices: readonly { device: string; host?: string }[] };
	subscribe(listener: () => void): () => void;
}

type Remote = Result<'policy.get'>['remotes'][number];

export interface QualityView {
	/** The remote that lends the selected device; '' for a device of this machine (or none selected). */
	remote: string;
	/** The toggle is shown: the selected device is a remote's and `policy.get` lists that remote. */
	shown: boolean;
	/** The remote's mark as last read. */
	fast: boolean;
	/** A write is in flight. */
	busy: boolean;
	/** The last write that failed. */
	error: Failure | null;
}

/** The entry with its mark set: `fast: true`, or the key removed for off (the file's default). */
function withFast(r: Remote, on: boolean): Remote {
	if (on) return { ...r, fast: true };
	const { fast: _fast, ...rest } = r;
	return rest;
}

export class RemoteQualityModel {
	#client: QualityClient;
	#devices: QualityDevices;
	#listeners = new Set<() => void>();
	#view: QualityView | null = null;
	#off: () => void;
	#known = ''; // the remote the mark below was read for; '' before any read
	#listed = false;
	#fast = false;
	#busy = false;
	#error: Failure | null = null;
	#gen = 0;

	constructor(client: QualityClient, devices: QualityDevices) {
		this.#client = client;
		this.#devices = devices;
		this.#off = devices.subscribe(() => this.#changed());
	}

	get view(): QualityView {
		if (this.#view === null) {
			const remote = this.#remote();
			this.#view = {
				remote,
				shown: remote !== '' && this.#known === remote && this.#listed,
				fast: this.#known === remote && this.#fast,
				busy: this.#busy,
				error: this.#error,
			};
		}
		return this.#view;
	}

	subscribe(listener: () => void): () => void {
		this.#listeners.add(listener);
		return () => {
			this.#listeners.delete(listener);
		};
	}

	dispose(): void {
		this.#off();
		this.#listeners.clear();
	}

	/** Reads the selected remote device's mark from `policy.get`; a device of this machine reads nothing. A newer read supersedes an older one. */
	async read(): Promise<void> {
		const remote = this.#remote();
		if (remote === '') return;
		const gen = ++this.#gen;
		try {
			const p = await this.#client.call('policy.get', {});
			if (gen !== this.#gen) return;
			this.#take(remote, p.remotes);
		} catch {
			// A lost connection is shown by the connection state; the policy always answers otherwise.
			return;
		}
		this.#changed();
	}

	/** The user's Fast toggle: the remote's entry is written with the mark, the rest of the list as it was read. */
	async setFast(on: boolean): Promise<void> {
		const remote = this.#remote();
		if (remote === '' || this.#busy) return;
		this.#busy = true;
		this.#error = null;
		this.#gen++; // an older read still on its way would show the old mark
		this.#changed();
		try {
			for (let attempt = 0; ; attempt++) {
				try {
					const p = await this.#client.call('policy.get', {});
					const i = p.remotes.findIndex((r) => r.name === remote);
					if (i < 0) {
						this.#error = { message: `${remote} is not in the remotes list, so its mark cannot be written` };
						this.#take(remote, p.remotes);
						break;
					}
					if ((p.remotes[i]!.fast === true) === on) {
						this.#take(remote, p.remotes); // already so: another client wrote it
						break;
					}
					const next = p.remotes.map((r, j) => (j === i ? withFast(r, on) : r));
					const r = await this.#client.call('policy.update', { remotes: next, ifRevision: p.revision });
					this.#take(remote, r.remotes);
					break;
				} catch (e) {
					if (e instanceof CapabilityError && e.code === 'POLICY_REVISION_CONFLICT' && attempt === 0) continue;
					// A lost connection is shown by the connection state; a capability error by its code.
					if (e instanceof CapabilityError) this.#error = failure(e);
					break;
				}
			}
		} finally {
			this.#busy = false;
			this.#changed();
		}
	}

	#take(remote: string, remotes: readonly Remote[]): void {
		const entry = remotes.find((r) => r.name === remote);
		this.#known = remote;
		this.#listed = entry !== undefined;
		this.#fast = entry?.fast === true;
	}

	/** The remote that lends the selected device. */
	#remote(): string {
		const { selected, devices } = this.#devices.view;
		return devices.find((d) => d.device === selected)?.host ?? '';
	}

	#changed(): void {
		this.#view = null;
		for (const l of [...this.#listeners]) l();
	}
}
