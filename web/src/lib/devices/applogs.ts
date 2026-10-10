// The app's logs (plan m2b step 6): `device.logs {device, applicationId}` for the
// launched or typed applicationId. `since` is left to the daemon, whose default is the
// last device.launch of that applicationId when one is recorded (so: since launch), and
// the whole window otherwise; the window the answer used is shown. Crashes come first
// (exception, message, causes, frames), then the lines. It is read on the user's request
// and when a launch, an install, a tap or a typed text ends (by any client) while logs
// are shown; never polled. Every device field leaves as a string for a text node.
import type { Params, Result } from '../api/capabilities.gen';
import { CapabilityError, type CallOptions } from '../api/client';
import { failure, type Failure } from '../build/failure';
import type { DevicesActivity } from './mirror';
import type { DeviceSelection } from './install';
import { UI_TRIGGERS } from './uitree';
import { inert, quoted } from './text';

export interface AppLogsClient {
	call(name: 'device.logs', params: Params<'device.logs'>, opts?: CallOptions): Promise<Result<'device.logs'>>;
}

/** Where the default applicationId comes from: the install and launch controls. */
export interface AppIdSource {
	view: { applicationId: string };
	subscribe(listener: () => void): () => void;
}

/** A working default (a constant, not a setting): lines kept inline, newest. The schema allows 1-1000. */
export const LOG_LINES = 200;

export interface CrashView {
	/** One line of text each: header, the exception and message, causes, frames. */
	lines: readonly string[];
}

export interface AppLogsView {
	device: string;
	/** The applicationId a read would use: typed, else the install panel's. */
	applicationId: string;
	loaded: boolean;
	loading: boolean;
	notes: readonly string[];
	crashes: readonly CrashView[];
	lines: readonly string[];
	error: Failure | null;
}

export function crashView(c: Result<'device.logs'>['crashes'][number]): CrashView {
	const lines = [`Crash of pid ${c.pid} at ${inert(c.time)}`, `${inert(c.exception)}: ${inert(c.message)}`];
	for (const x of c.causes ?? []) lines.push(`Caused by: ${inert(x)}`);
	for (const f of c.frames ?? []) lines.push(`  at ${inert(f)}`);
	return { lines };
}

export function logLine(l: Result<'device.logs'>['lines'][number]): string {
	return `${inert(l.time)} ${l.pid}/${l.tid} ${l.priority.charAt(0).toUpperCase()} ${inert(l.tag)}: ${inert(l.message)}${l.crash ? ' [crash]' : ''}`;
}

function notesOf(r: Result<'device.logs'>): string[] {
	const n: string[] = [];
	n.push(`Window ${inert(r.window.from)} to ${inert(r.window.to)}.`);
	const f = r.filter;
	n.push(
		f.by === 'none'
			? 'No applicationId filter.'
			: `${quoted(f.applicationId ?? '')} selected by ${f.by}${f.pids ? `, pids ${f.pids.join(' ')}` : ''}.`,
	);
	n.push(`${r.counts.returned} lines shown of ${r.counts.matched} matched, ${r.counts.scanned} scanned${r.truncated ? ' (truncated)' : ''}.`);
	if (r.emptyReason) n.push(`No lines: ${inert(r.emptyReason)}.`);
	for (const w of r.warnings ?? []) n.push(`Warning: ${inert(w)}`);
	return n;
}

export class AppLogsModel {
	#client: AppLogsClient;
	#devices: DeviceSelection;
	#source: AppIdSource;
	#listeners = new Set<() => void>();
	#view: AppLogsView | null = null;
	#offs: (() => void)[] = [];
	#typed: string | null = null;
	#result: Result<'device.logs'> | null = null;
	/** The device the result (or the read in flight) belongs to. */
	#for = '';
	#loading = false;
	#error: Failure | null = null;
	#gen = 0;

	constructor(client: AppLogsClient, devices: DeviceSelection, source: AppIdSource, activity: Pick<DevicesActivity, 'onTerminal'>) {
		this.#client = client;
		this.#devices = devices;
		this.#source = source;
		this.#offs.push(devices.subscribe(() => this.#deviceChanged()));
		this.#offs.push(source.subscribe(() => this.#changed()));
		this.#offs.push(activity.onTerminal(UI_TRIGGERS, () => void this.#reload()));
	}

	get view(): AppLogsView {
		this.#view ??= this.#build();
		return this.#view;
	}

	subscribe(listener: () => void): () => void {
		this.#listeners.add(listener);
		return () => {
			this.#listeners.delete(listener);
		};
	}

	/** Typing replaces the install panel's applicationId; emptied, it follows it again. */
	setApplicationId(id: string): void {
		this.#typed = id === '' ? null : id;
		this.#changed();
	}

	#applicationId(): string {
		return (this.#typed ?? this.#source.view.applicationId).trim();
	}

	/** `device.logs`, on the user's request. */
	async load(): Promise<void> {
		const device = this.#devices.view.selected;
		const applicationId = this.#applicationId();
		if (device === '' || applicationId === '') return;
		const gen = ++this.#gen;
		this.#for = device;
		this.#loading = true;
		this.#error = null;
		this.#changed();
		try {
			const r = await this.#client.call('device.logs', { device, applicationId, maxLines: LOG_LINES });
			if (gen !== this.#gen) return;
			this.#result = r;
		} catch (e) {
			if (gen !== this.#gen) return;
			if (!(e instanceof CapabilityError)) {
				this.#loading = false;
				this.#changed();
				return;
			}
			this.#error = failure(e);
		}
		this.#loading = false;
		this.#changed();
	}

	dispose(): void {
		for (const off of this.#offs) off();
		this.#offs = [];
	}

	/** Only while something is shown for the selected device: `#for` is set by a read and cleared with the device. */
	#reload(): Promise<void> {
		return this.#for !== '' && this.#for === this.#devices.view.selected ? this.load() : Promise.resolve();
	}

	#deviceChanged(): void {
		if (this.#for !== this.#devices.view.selected) {
			this.#gen++;
			this.#for = '';
			this.#result = null;
			this.#error = null;
			this.#loading = false;
		}
		this.#changed();
	}

	#build(): AppLogsView {
		const r = this.#result;
		return {
			device: this.#devices.view.selected,
			applicationId: this.#typed ?? this.#source.view.applicationId,
			loaded: r !== null || this.#error !== null,
			loading: this.#loading,
			notes: r ? notesOf(r) : [],
			crashes: r ? r.crashes.map(crashView) : [],
			lines: r ? r.lines.map(logLine) : [],
			error: this.#error,
		};
	}

	#changed(): void {
		this.#view = null;
		for (const l of [...this.#listeners]) l();
	}
}
