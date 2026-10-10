// The project picker's model (ADR-0045 §3, §4): the projects this installation has known,
// live. `workspace.watch` answers with the index merged with the live states, and its
// `workspace.state` events keep the rows current, so a workspace an agent opens through MCP
// shows without a reload. Choosing a row opens it by id; "Open folder..." raises the
// daemon's folder dialog (`workspace.chooseRoot`) and opens the root it answers with; where
// the dialog is unavailable the user types a root, as 2b did. Opening never trusts.
//
// The watch lives while the picker is shown (start/stop), so every visit reads the index
// and the trusted marks anew; a lost connection or `events.gap` starts it again.
import { eventTopics, type EventPayload, type Params, type Result } from '../api/capabilities.gen';
import {
	ConnectionError,
	type CallOptions,
	type ConnectionState,
	type EventFrame,
	type SubscribeHandlers,
	type Subscription,
} from '../api/client';
import { failure, failureText, type Failure } from '../build/failure';

/** The daemon's list is at most 50 (workspace.watch); events never grow it past that. */
export const MAX_ROWS = 50;

const TOPIC_STATE = eventTopics['workspace.watch'];
const TOPIC_GAP = 'events.gap';

type Calls = 'workspace.open' | 'workspace.chooseRoot' | 'system.capabilities' | 'events.unsubscribe';
type StateEvent = EventPayload<'workspace.watch', 'state'>;

export interface PickerClient {
	call<N extends Calls>(name: N, params: Params<N>, opts?: CallOptions): Promise<Result<N>>;
	subscribe(
		name: 'workspace.watch',
		params: Params<'workspace.watch'>,
		handlers: SubscribeHandlers,
	): Promise<Subscription & { readonly result: Result<'workspace.watch'> }>;
}

export interface PickerRow {
	id: string;
	name: string;
	root: string;
	lastOpenedAt: string;
	state: string;
	trusted: boolean;
	/** Open in the daemon now: anything but closed. */
	open: boolean;
}

export type Chooser = 'unknown' | 'available' | 'unavailable';
export type WatchState = 'idle' | 'starting' | 'watching' | 'waiting' | 'error' | 'ended';

export interface PickerView {
	rows: readonly PickerRow[];
	truncated: boolean;
	watch: { state: WatchState; detail: string };
	/** Whether the folder dialog can be raised; anything but available shows the typed field. */
	chooser: Chooser;
	chooserReason: string;
	/** A dialog is on the user's screen. */
	choosing: boolean;
	/** An open call is in flight. */
	opening: boolean;
	error: Failure | null;
}

export class PickerModel {
	#client: PickerClient;
	#navigate: (workspaceId: string) => void;
	#rows = new Map<string, PickerRow>();
	#truncated = false;
	#watch: { state: WatchState; detail: string } = { state: 'idle', detail: '' };
	#chooser: Chooser = 'unknown';
	#chooserReason = '';
	#choosing = false;
	#opening = false;
	#error: Failure | null = null;
	#run: object | null = null;
	#sub: Subscription | null = null;
	#waiting = false;
	#listeners = new Set<() => void>();
	#view: PickerView | null = null;

	constructor(client: PickerClient, navigate: (workspaceId: string) => void) {
		this.#client = client;
		this.#navigate = navigate;
	}

	get view(): PickerView {
		this.#view ??= {
			rows: [...this.#rows.values()].sort((a, b) => time(b.lastOpenedAt) - time(a.lastOpenedAt)),
			truncated: this.#truncated,
			watch: this.#watch,
			chooser: this.#chooser,
			chooserReason: this.#chooserReason,
			choosing: this.#choosing,
			opening: this.#opening,
			error: this.#error,
		};
		return this.#view;
	}

	subscribe(listener: () => void): () => void {
		this.#listeners.add(listener);
		return () => {
			this.#listeners.delete(listener);
		};
	}

	/** The picker is shown: read what the dialog can do, and follow the workspaces. */
	start(): void {
		void this.#readChooser();
		this.#begin();
	}

	/** The picker is left: the watch ends; the rows stay until the next start replaces them. */
	stop(): void {
		this.#run = null;
		this.#waiting = false;
		this.#release();
		this.#setWatch('idle', '');
	}

	onConnection(state: ConnectionState): void {
		if (state === 'open' && this.#waiting) {
			this.#waiting = false;
			this.#begin();
			void this.#readChooser();
		} else if ((state === 'unauthorized' || state === 'closed') && this.#run !== null) {
			this.#waiting = false;
			this.#setWatch('ended', state === 'unauthorized' ? 'the daemon refused the token' : 'the connection was closed');
		}
	}

	/** Why the project the address named could not be opened. */
	showError(f: Failure): void {
		this.#error = f;
		this.#changed();
	}

	/** A click on a row: the workspace is opened by its id, then the tab shows it. */
	openRow(id: string): Promise<void> {
		return this.#open(id);
	}

	/** The typed root, where the folder dialog is unavailable. */
	openRoot(root: string): Promise<void> {
		return this.#open(root.trim());
	}

	/** "Open folder...": the daemon's dialog; a dismissed dialog opens nothing. */
	async chooseFolder(): Promise<void> {
		if (this.#choosing || this.#opening) return;
		this.#choosing = true;
		this.#error = null;
		this.#changed();
		let root: string | undefined;
		try {
			const r = await this.#client.call('workspace.chooseRoot', {});
			if (!r.dismissed) root = r.root;
		} catch (e) {
			this.#error = failure(e);
		} finally {
			this.#choosing = false;
			this.#changed();
		}
		if (root !== undefined && root !== '') await this.#open(root);
	}

	async #open(target: string): Promise<void> {
		if (target === '' || this.#opening) return;
		this.#opening = true;
		this.#error = null;
		this.#changed();
		let id: string | undefined;
		try {
			id = (await this.#client.call('workspace.open', {}, { workspace: target })).id;
		} catch (e) {
			this.#error = failure(e);
		} finally {
			this.#opening = false;
			this.#changed();
		}
		if (id !== undefined) this.#navigate(id);
	}

	async #readChooser(): Promise<void> {
		try {
			const r = await this.#client.call('system.capabilities', {});
			const c = r.capabilities?.find((x) => x.name === 'workspace.chooseRoot');
			this.#chooser = c?.available === true ? 'available' : 'unavailable';
			this.#chooserReason = c?.available === true ? '' : (c?.unavailableReason ?? '');
			this.#changed();
		} catch {
			// Unknown stays: the typed field is offered, it always works.
		}
	}

	// --- the watch -------------------------------------------------------------------

	#begin(): void {
		const run = {};
		this.#run = run;
		this.#waiting = false;
		this.#release();
		this.#setWatch('starting', '');
		const handlers: SubscribeHandlers = {
			onEvent: (frame) => {
				if (this.#run === run) this.#event(frame);
			},
			onEnd: (why) => {
				if (this.#run !== run) return;
				this.#sub = null;
				if (why.kind === 'lost') {
					this.#waiting = true;
					this.#setWatch('waiting', 'the connection was lost');
				} else {
					this.#setWatch('ended', why.message);
				}
			},
		};
		this.#client.subscribe('workspace.watch', {}, handlers).then(
			(sub) => {
				if (this.#run !== run) {
					this.#unsubscribe(sub);
					return;
				}
				this.#sub = sub;
				// The list was taken after the subscription opened, and the session writes the
				// first event only after this answer: the snapshot replaces the rows and the
				// events merge into it by id.
				this.#rows = new Map(sub.result.workspaces.map((w) => [w.id, { ...w, open: w.state !== 'closed' }]));
				this.#truncated = sub.result.truncated;
				this.#setWatch('watching', '');
			},
			(err: unknown) => {
				if (this.#run !== run) return;
				if (err instanceof ConnectionError && err.kind === 'lost') {
					this.#waiting = true;
					this.#setWatch('waiting', 'the connection was lost');
				} else {
					this.#setWatch(err instanceof ConnectionError ? 'ended' : 'error', failureText(failure(err)));
				}
			},
		);
	}

	#event(frame: EventFrame): void {
		const ev = frame.event;
		if (ev.topic === TOPIC_GAP) {
			// Events were lost: the list is read again (workspace.watch's contract).
			this.#begin();
			return;
		}
		if (ev.topic !== TOPIC_STATE || ev.workspace === null) return;
		const p = ev.payload as StateEvent;
		const prev = this.#rows.get(ev.workspace);
		this.#rows.set(ev.workspace, {
			id: ev.workspace,
			name: p.name,
			root: p.root,
			// An open refreshes the index's lastOpenedAt; a row the snapshot did not hold is new.
			lastOpenedAt: prev === undefined || p.state === 'opening' ? ev.time : prev.lastOpenedAt,
			state: p.state,
			trusted: p.trusted,
			open: p.state !== 'closed',
		});
		if (this.#rows.size > MAX_ROWS) {
			const oldest = [...this.#rows.values()].sort((a, b) => time(a.lastOpenedAt) - time(b.lastOpenedAt))[0];
			if (oldest) this.#rows.delete(oldest.id);
		}
		this.#changed();
	}

	#setWatch(state: WatchState, detail: string): void {
		this.#watch = { state, detail };
		this.#changed();
	}

	#release(): void {
		const sub = this.#sub;
		this.#sub = null;
		if (sub) this.#unsubscribe(sub);
	}

	#unsubscribe(sub: Subscription): void {
		sub.stop();
		this.#client.call('events.unsubscribe', { subscriptionId: sub.subscriptionId }).catch(() => {
			// The session may be gone; its subscriptions went with it.
		});
	}

	#changed(): void {
		this.#view = null;
		for (const l of [...this.#listeners]) l();
	}
}

const time = (iso: string): number => {
	const t = Date.parse(iso);
	return Number.isNaN(t) ? 0 : t;
};
