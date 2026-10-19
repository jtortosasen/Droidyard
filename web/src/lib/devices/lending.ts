// What this machine lends (plan m2t step 7; ADR-0099, ADR-0104): `policy.get`'s `hosting.lend`,
// read when the connection opens and when the panel is refreshed, and Stop lending, which is
// the only thing the UI writes of it. Merge Patch replaces an array whole, so the entries
// that stay must be sent back with the revision they were read at, as the trust grant does;
// the policy is read again after a concurrent edit (POLICY_REVISION_CONFLICT) and the removal
// decided again, once, since it is the same removal of the same device. Who holds a lent
// device is not here: it is a session row (`deviceHolds`), and Release is the sessions model's.
import type { Params, Result } from '../api/capabilities.gen';
import { CapabilityError, type CallOptions } from '../api/client';
import { failure, type Failure } from '../build/failure';
import type { DeviceHold } from '../projects/sessions';

type Calls = 'policy.get' | 'policy.update';

export interface LendingClient {
	call<N extends Calls>(name: N, params: Params<N>, opts?: CallOptions): Promise<Result<N>>;
}

export type Lent = Result<'policy.get'>['hosting']['lend'][number];

export interface LendingView {
	/** The devices this machine lends, and to whom. */
	lent: readonly Lent[];
	/** The last Stop lending that failed. */
	error: Failure | null;
}

export class LendingModel {
	#client: LendingClient;
	#listeners = new Set<() => void>();
	#view: LendingView | null = null;
	#lent: readonly Lent[] = [];
	#error: Failure | null = null;
	#gen = 0;

	constructor(client: LendingClient) {
		this.#client = client;
	}

	get view(): LendingView {
		this.#view ??= { lent: this.#lent, error: this.#error };
		return this.#view;
	}

	subscribe(listener: () => void): () => void {
		this.#listeners.add(listener);
		return () => {
			this.#listeners.delete(listener);
		};
	}

	/** `policy.get`; a newer read supersedes an older one still on its way. */
	async refresh(): Promise<void> {
		const gen = ++this.#gen;
		try {
			const p = await this.#client.call('policy.get', {});
			if (gen !== this.#gen) return;
			this.#lent = p.hosting.lend;
		} catch {
			// A lost connection is shown by the connection state; the policy always answers otherwise.
			return;
		}
		this.#changed();
	}

	/** The user's Stop lending of `serial`: its entry leaves `hosting.lend`, the others are sent back. */
	async stopLending(serial: string): Promise<void> {
		this.#error = null;
		this.#gen++; // an older read still on its way would show the device lent again
		for (let attempt = 0; ; attempt++) {
			try {
				const p = await this.#client.call('policy.get', {});
				const rest = p.hosting.lend.filter((l) => l.device !== serial);
				if (rest.length !== p.hosting.lend.length) {
					const r = await this.#client.call('policy.update', { hosting: { lend: rest }, ifRevision: p.revision });
					this.#lent = r.hosting.lend;
				} else {
					this.#lent = p.hosting.lend; // another client already stopped it
				}
				break;
			} catch (e) {
				if (e instanceof CapabilityError && e.code === 'POLICY_REVISION_CONFLICT' && attempt === 0) continue;
				// A lost connection is shown by the connection state; a capability error by its code.
				if (e instanceof CapabilityError) this.#error = failure(e);
				break;
			}
		}
		this.#changed();
	}

	#changed(): void {
		this.#view = null;
		for (const l of [...this.#listeners]) l();
	}
}

export interface LentRow {
	serial: string;
	/** "lent to anna, bob". */
	text: string;
	/** The session that holds it now (a peer's link is a session row, ADR-0104 §1); undefined when free. */
	holder: DeviceHold | undefined;
}

/** The lent devices as the panel shows them, each with its holder from the live session rows. */
export function lentRows(lent: readonly Lent[], holds: readonly DeviceHold[]): LentRow[] {
	return lent.map((l) => ({
		serial: l.device,
		// An empty `to` is a share no one has joined yet (ADR-0108 §2).
		text: l.to.length > 0 ? `lent to ${l.to.join(', ')}` : 'shared, no one joined yet',
		holder: holds.find((h) => h.serial === l.device),
	}));
}
