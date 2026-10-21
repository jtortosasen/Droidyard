// The UI recordings' model (plan m2x step 8; ADR-0113 §6): the recordings of every device the
// daemon keeps, live from `device.watchUiRecordings`, with Record and Stop for the toolbar's
// device, Download and Delete for a row. A pure state machine with no markup and no socket, so a
// test drives it with a fake client. It watches only while the panel can be seen and
// unsubscribes otherwise. Every field it keeps is the daemon's (counts, times, the end reason and
// its cause): a recording carries no device text (ADR-0113 §6), and the timeline's bytes go to a
// file, never to the page.
import type { EventPayload, Params, Result } from '../api/capabilities.gen';
import { eventTopics } from '../api/capabilities.gen';
import {
	ConnectionError,
	type ConnectionState,
	type SubscribeHandlers,
	type Subscription,
} from '../api/client';
import { failure, failureText } from '../build/failure';

/**
 * A recording as the daemon publishes it. `remote` and `copied` are the record's own fields for a
 * recording of a lent device: not in the published schema, so read only if present.
 */
export type UiRecording = Result<'device.uiRecordings'>['recordings'][number] & {
	remote?: boolean;
	copied?: boolean;
};

export interface UiRecordingClient {
	subscribe(
		name: 'device.watchUiRecordings',
		params: Params<'device.watchUiRecordings'>,
		handlers: SubscribeHandlers,
	): Promise<Subscription & { readonly result: Result<'device.watchUiRecordings'> }>;
	call(name: 'device.recordUi', params: Params<'device.recordUi'>): Promise<Result<'device.recordUi'>>;
	call(name: 'device.stopUiRecording', params: Params<'device.stopUiRecording'>): Promise<Result<'device.stopUiRecording'>>;
	call(name: 'device.deleteUiRecording', params: Params<'device.deleteUiRecording'>): Promise<Result<'device.deleteUiRecording'>>;
	call(name: 'device.uiRecordings', params: Params<'device.uiRecordings'>): Promise<Result<'device.uiRecordings'>>;
	call(name: 'events.unsubscribe', params: Params<'events.unsubscribe'>): Promise<Result<'events.unsubscribe'>>;
}

export interface UiRecordingDevices {
	view: { selected: string };
	subscribe(listener: () => void): () => void;
}

/** What a download needs of the page: `fetch`, the per-launch token, and a way to hand the file over. */
export interface UiRecordingDownload {
	fetch: (url: string, init: { headers: Record<string, string> }) => Promise<Pick<Response, 'ok' | 'status' | 'blob' | 'text'>>;
	token: string;
	/** Saves the bytes as a file of this name (an anchor with `download`). */
	save: (blob: Blob, name: string) => void;
}

export type UiRecordingState = 'idle' | 'watching' | 'failed';

export interface UiRecordingView {
	state: UiRecordingState;
	/** Why the list is not live; '' for none. */
	cause: string;
	device: string;
	/** Newest first. */
	recordings: readonly UiRecording[];
	/** The toolbar device's running recording, if any. */
	running: UiRecording | null;
	/** A Record, Stop or Delete is on its way. */
	busy: boolean;
	/** The last action's failure, as text; '' for none. */
	error: string;
	/** The recording whose Delete awaits its confirmation. */
	confirming: string | null;
	/** The recording being downloaded. */
	downloading: string | null;
}

const TOPIC = eventTopics['device.watchUiRecordings'];

type StatePush = EventPayload<'device.watchUiRecordings', 'state'>;
type DeletedPush = EventPayload<'device.watchUiRecordings', 'deleted'>;

/** Whether Download is offered: the recording ended and, for a remote one, its timeline is copied here. */
export function canDownload(r: UiRecording): boolean {
	return r.state === 'ended' && !(r.remote === true && r.copied !== true);
}

/** Whole seconds a recording has run (or ran), never negative. */
export function elapsedSeconds(r: UiRecording, now: number): number {
	const start = Date.parse(r.startedAt);
	const end = r.state === 'recording' || !r.endedAt ? now : Date.parse(r.endedAt);
	if (Number.isNaN(start) || Number.isNaN(end)) return 0;
	return Math.max(0, Math.floor((end - start) / 1000));
}

/** `m:ss`, or `h:mm:ss` from an hour. */
export function clockText(seconds: number): string {
	const s = Math.max(0, Math.floor(seconds));
	const h = Math.floor(s / 3600);
	const m = Math.floor((s % 3600) / 60);
	const ss = String(s % 60).padStart(2, '0');
	return h > 0 ? `${h}:${String(m).padStart(2, '0')}:${ss}` : `${m}:${ss}`;
}

function newestFirst(list: UiRecording[]): UiRecording[] {
	return list.sort((a, b) => Date.parse(b.startedAt) - Date.parse(a.startedAt));
}

interface Run {
	ended: boolean;
}

interface Pushed {
	state?: UiRecording;
	deleted?: string;
}

export class UiRecordingModel {
	#client: UiRecordingClient;
	#devices: UiRecordingDevices;
	#download: UiRecordingDownload;
	#listeners = new Set<() => void>();
	#view: UiRecordingView | null = null;
	#off: () => void;

	#active = false;
	#state: UiRecordingState = 'idle';
	#cause = '';
	#recordings: UiRecording[] = [];
	#busy = false;
	#error = '';
	#confirming: string | null = null;
	#downloading: string | null = null;
	#run: Run | null = null;
	#sub: Subscription | null = null;
	#buffer: Pushed[] | null = null; // events that arrived before the snapshot
	#waiting = false;

	constructor(client: UiRecordingClient, devices: UiRecordingDevices, download: UiRecordingDownload) {
		this.#client = client;
		this.#devices = devices;
		this.#download = download;
		this.#off = devices.subscribe(() => this.#changed());
	}

	get view(): UiRecordingView {
		this.#view ??= {
			state: this.#state,
			cause: this.#cause,
			device: this.#devices.view.selected,
			recordings: this.#recordings,
			running: this.#recordings.find((r) => r.device === this.#devices.view.selected && r.state === 'recording') ?? null,
			busy: this.#busy,
			error: this.#error,
			confirming: this.#confirming,
			downloading: this.#downloading,
		};
		return this.#view;
	}

	subscribe(listener: () => void): () => void {
		this.#listeners.add(listener);
		return () => {
			this.#listeners.delete(listener);
		};
	}

	/** The panel is shown (its region open, the page visible) or not. */
	setActive(active: boolean): void {
		if (active === this.#active) return;
		this.#active = active;
		if (active) {
			if (this.#state !== 'watching') this.#begin();
		} else {
			this.#release();
			this.#waiting = false;
			this.#state = 'idle';
			this.#cause = '';
			this.#confirming = null;
			this.#changed();
		}
	}

	onConnection(state: ConnectionState): void {
		if (state === 'open' && this.#waiting && this.#active) {
			this.#waiting = false;
			this.#begin();
		} else if ((state === 'unauthorized' || state === 'closed') && this.#state === 'watching') {
			this.#release();
			this.#fail(state === 'unauthorized' ? 'the daemon refused the token' : 'the connection was closed');
		}
	}

	/** After a failure: watches again. */
	restart(): void {
		if (this.#state === 'failed' && this.#active) this.#begin();
	}

	/** Starts recording the toolbar's device. */
	async record(): Promise<void> {
		const device = this.#devices.view.selected;
		if (device === '' || this.#busy) return;
		await this.#action(async () => {
			this.#upsert(await this.#client.call('device.recordUi', { device }));
		});
	}

	/** Ends the toolbar device's recording. */
	async stop(): Promise<void> {
		const device = this.#devices.view.selected;
		if (device === '' || this.#busy) return;
		await this.#action(async () => {
			const r = await this.#client.call('device.stopUiRecording', { device });
			if (r.recording) this.#upsert(r.recording);
		});
	}

	/** Delete asks first: nothing is deleted until `confirmDelete`. */
	askDelete(id: string): void {
		this.#confirming = id;
		this.#changed();
	}

	cancelDelete(): void {
		this.#confirming = null;
		this.#changed();
	}

	async confirmDelete(): Promise<void> {
		const id = this.#confirming;
		if (id === null || this.#busy) return;
		this.#confirming = null;
		await this.#action(async () => {
			await this.#client.call('device.deleteUiRecording', { id });
			this.#remove(id);
		});
	}

	/** Fetches the finished timeline with the token and hands it to `save` as `<id>.jsonl`. */
	async downloadRecording(id: string): Promise<void> {
		const r = this.#recordings.find((x) => x.id === id);
		if (!r || !canDownload(r) || this.#downloading !== null) return;
		this.#downloading = id;
		this.#error = '';
		this.#changed();
		try {
			const res = await this.#download.fetch(`/v1/artifacts/r.${encodeURIComponent(id)}`, {
				headers: { Authorization: `Bearer ${this.#download.token}` },
			});
			if (!res.ok) throw new Error(await httpFailure(res));
			this.#download.save(await res.blob(), `${id}.jsonl`);
		} catch (err) {
			this.#error = failureText(failure(err));
		} finally {
			this.#downloading = null;
			this.#changed();
		}
	}

	dispose(): void {
		this.#off();
		this.#release();
		this.#listeners.clear();
	}

	async #action(run: () => Promise<void>): Promise<void> {
		this.#busy = true;
		this.#error = '';
		this.#changed();
		try {
			await run();
		} catch (err) {
			this.#error = failureText(failure(err));
		} finally {
			this.#busy = false;
			this.#changed();
		}
	}

	#begin(): void {
		this.#release();
		const run: Run = { ended: false };
		this.#run = run;
		this.#waiting = false;
		this.#buffer = [];
		this.#state = 'watching';
		this.#cause = '';
		this.#changed();

		const handlers: SubscribeHandlers = {
			onEvent: (frame) => {
				const ev = frame.event;
				if (this.#run !== run || run.ended) return;
				if (ev.topic === TOPIC) {
					if (ev.type === 'state') this.#push({ state: (ev.payload as Partial<StatePush>).recording as UiRecording | undefined });
					else if (ev.type === 'deleted') this.#push({ deleted: (ev.payload as Partial<DeletedPush>).id });
				} else if (ev.type === 'gap') {
					void this.#resnapshot(run); // events were lost: the list is read again
				}
			},
			onEnd: (why) => {
				if (this.#run !== run || run.ended) return;
				this.#sub = null;
				if (why.kind === 'lost') {
					this.#waiting = true;
					this.#changed();
				} else {
					this.#release();
					this.#fail(why.message);
				}
			},
		};
		this.#client.subscribe('device.watchUiRecordings', {}, handlers).then(
			(sub) => {
				if (this.#run !== run || run.ended) {
					this.#unsubscribe(sub);
					return;
				}
				this.#sub = sub;
				const buffered = this.#buffer ?? [];
				this.#buffer = null;
				this.#recordings = newestFirst([...(sub.result.recordings as UiRecording[])]);
				for (const b of buffered) this.#apply(b, true);
				this.#changed();
			},
			(err: unknown) => {
				if (this.#run !== run || run.ended) return;
				if (err instanceof ConnectionError && err.kind === 'lost') {
					this.#waiting = true;
					this.#changed();
				} else {
					this.#release();
					this.#fail(failureText(failure(err)));
				}
			},
		);
	}

	async #resnapshot(run: Run): Promise<void> {
		try {
			const r = await this.#client.call('device.uiRecordings', {});
			if (this.#run !== run || run.ended) return;
			this.#recordings = newestFirst([...(r.recordings as UiRecording[])]);
			this.#changed();
		} catch {
			// The next event or reconnect brings the list right.
		}
	}

	#push(e: Pushed): void {
		if (this.#buffer) this.#buffer.push(e);
		else {
			this.#apply(e, false);
			this.#changed();
		}
	}

	/** `fromBuffer`: an event older than the snapshot must not undo it (an ended recording stays ended). */
	#apply(e: Pushed, fromBuffer: boolean): void {
		if (typeof e.deleted === 'string') {
			this.#remove(e.deleted);
		} else if (e.state && typeof e.state.id === 'string') {
			const id = e.state.id;
			if (fromBuffer && this.#recordings.find((r) => r.id === id)?.state === 'ended') return;
			this.#upsert(e.state);
		}
	}

	#upsert(r: UiRecording): void {
		this.#recordings = newestFirst([r, ...this.#recordings.filter((x) => x.id !== r.id)]);
		this.#changed();
	}

	#remove(id: string): void {
		this.#recordings = this.#recordings.filter((r) => r.id !== id);
		if (this.#confirming === id) this.#confirming = null;
		this.#changed();
	}

	#fail(cause: string): void {
		this.#state = 'failed';
		this.#cause = cause;
		this.#waiting = false;
		this.#changed();
	}

	#release(): void {
		if (this.#run) this.#run.ended = true;
		this.#run = null;
		this.#buffer = null;
		const sub = this.#sub;
		this.#sub = null;
		if (sub) this.#unsubscribe(sub);
	}

	#unsubscribe(sub: Subscription): void {
		sub.stop();
		this.#client.call('events.unsubscribe', { subscriptionId: sub.subscriptionId }).catch(() => {
			// The session may be gone, and then so is the subscription.
		});
	}

	#changed(): void {
		this.#view = null;
		for (const l of [...this.#listeners]) l();
	}
}

/** The daemon's words for a refused download: its JSON error body, else the status. */
async function httpFailure(res: Pick<Response, 'status' | 'text'>): Promise<string> {
	try {
		const body = JSON.parse(await res.text()) as { code?: unknown; message?: unknown };
		if (typeof body.message === 'string') return typeof body.code === 'string' ? `${body.code}: ${body.message}` : body.message;
	} catch {
		// Not the daemon's error body.
	}
	return `the download failed (HTTP ${res.status})`;
}
