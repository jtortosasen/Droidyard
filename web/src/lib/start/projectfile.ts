// The project file (ADR-0046): `<root>/.droidyard/project.json` keeps the UI's choices for
// a project (module and variant, the AVD or serial, the application id), so nothing is
// typed again in a new tab. It is read and written by the UI through `file.read` and
// `file.write`; no capability interprets it.
//
// Rules this module keeps, each tested:
//   - the file only preselects: a value the daemon's lists lack is ignored by the models
//     that apply it, and nothing here carries trust, policy, admission or tool keys;
//   - it is read when the workspace opens and never written on load: the first write is
//     the first change the user makes (D19). Every write reads it again first and patches
//     what it read, with ifRevision (ADR-0075 §7): a key another writer set since the load
//     (a hand edit, a second tab) survives;
//   - a file with a newer schemaVersion, one that does not parse, and one that could not
//     be read are treated as absent and never overwritten; the panel says so and the
//     user's choices last for the tab;
//   - unknown keys survive a rewrite (a newer sibling tool's, or a hand edit);
//   - a failed write keeps the choice (it lives in the models) and shows the failure.
import type { Params, Result } from '../api/capabilities.gen';
import { CapabilityError, type CallOptions } from '../api/client';
import { failure, type Failure } from '../build/failure';

export const PROJECT_FILE = '.droidyard/project.json';
export const SCHEMA_VERSION = 1;
/** file.read's largest page: a project file is a few hundred bytes, a bigger one is not ours. */
const READ_LIMIT = 262144;
const MAX_TEXT = 256;
/** An Android application id: dotted Java identifiers, at least two segments. */
const APPLICATION_ID = /^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z][A-Za-z0-9_]*)+$/;

export type DevicePref = { avd: string } | { serial: string };

/** What the file holds that the UI may preselect; every field is checked, none is trusted. */
export interface ProjectPrefs {
	build?: { module?: string; variant?: string };
	device?: DevicePref;
	applicationId?: string;
}

/** One change the user made: the keys it names are replaced, the rest of the file is kept. */
export interface ProjectPatch {
	build?: { module: string; variant: string };
	device?: DevicePref;
	applicationId?: string;
	/** The Gradle root, relative to the project root (ADR-0075 §7); the UI never reads it back from the file. */
	gradleRoot?: string;
}

export type FileState =
	| 'idle' // not read yet (or the connection was lost mid-read)
	| 'loading'
	| 'absent' // no file: the defaults apply, nothing is written until a change
	| 'loaded'
	| 'newer' // schemaVersion above ours
	| 'invalid' // does not parse, or is not ours
	| 'unreadable'; // the daemon refused the read

/** The states in which the file is never written. */
const BLOCKED: readonly FileState[] = ['newer', 'invalid', 'unreadable'];
/** A write that meets a conflict reads and patches again; this many tries in all (as trust.ts). */
const WRITE_TRIES = 3;

type ReadOutcome =
	| { kind: 'ok'; raw: Record<string, unknown>; revision: string | undefined }
	| { kind: 'absent' }
	| { kind: 'newer' | 'invalid' | 'unreadable'; notice: string };

export type Parsed = { kind: 'ok'; raw: Record<string, unknown> } | { kind: 'newer'; version: number } | { kind: 'invalid' };

const isObject = (v: unknown): v is Record<string, unknown> => typeof v === 'object' && v !== null && !Array.isArray(v);

/** A name from the file: a short string without control characters, else ignored. */
function text(v: unknown): string | undefined {
	if (typeof v !== 'string' || v === '' || v.length > MAX_TEXT) return undefined;
	// eslint-disable-next-line no-control-regex
	return /[\u0000-\u001f\u007f]/.test(v) ? undefined : v;
}

export function validApplicationId(v: unknown): v is string {
	return typeof v === 'string' && v.length <= MAX_TEXT && APPLICATION_ID.test(v);
}

export function parseProjectFile(content: string): Parsed {
	let raw: unknown;
	try {
		raw = JSON.parse(content);
	} catch {
		return { kind: 'invalid' };
	}
	if (!isObject(raw)) return { kind: 'invalid' };
	const v = raw.schemaVersion;
	if (typeof v !== 'number' || !Number.isSafeInteger(v) || v < 1) return { kind: 'invalid' };
	if (v > SCHEMA_VERSION) return { kind: 'newer', version: v };
	return { kind: 'ok', raw };
}

/** The values of a parsed file the UI may use; a field of the wrong shape is simply absent. */
export function prefsOf(raw: Record<string, unknown>): ProjectPrefs {
	const prefs: ProjectPrefs = {};
	if (isObject(raw.build)) {
		const module = text(raw.build.module);
		const variant = text(raw.build.variant);
		if (module !== undefined || variant !== undefined) prefs.build = { module, variant };
	}
	if (isObject(raw.device)) {
		const avd = text(raw.device.avd);
		const serial = text(raw.device.serial);
		if (avd !== undefined) prefs.device = { avd };
		else if (serial !== undefined) prefs.device = { serial };
	}
	if (validApplicationId(raw.applicationId)) prefs.applicationId = raw.applicationId;
	return prefs;
}

/** The file with a change applied; every key the change does not name is kept as it was. */
export function applyPatch(raw: Record<string, unknown>, patch: ProjectPatch): Record<string, unknown> {
	const next: Record<string, unknown> = { ...raw, schemaVersion: SCHEMA_VERSION };
	if (patch.build) next.build = { ...(isObject(raw.build) ? raw.build : {}), module: patch.build.module, variant: patch.build.variant };
	if (patch.device) {
		// avd and serial exclude each other; a key of a newer shape next to them stays.
		const { avd: _avd, serial: _serial, ...rest } = isObject(raw.device) ? raw.device : ({} as Record<string, unknown>);
		next.device = { ...rest, ...patch.device };
	}
	if (patch.applicationId !== undefined) next.applicationId = patch.applicationId;
	if (patch.gradleRoot !== undefined) next.gradleRoot = patch.gradleRoot;
	return next;
}

export const render = (raw: Record<string, unknown>): string => JSON.stringify(raw, null, 2) + '\n';

/** The device a choice of this list entry is stored as; an emulator whose AVD is unknown is not. */
export function devicePrefOf(d: { device: string; kind: 'emulator' | 'physical'; avdName?: string }): DevicePref | null {
	if (d.kind === 'physical') return { serial: d.device };
	return d.avdName ? { avd: d.avdName } : null;
}

type Calls = 'file.read' | 'file.write';

export interface ProjectFileClient {
	call<N extends Calls>(name: N, params: Params<N>, opts?: CallOptions): Promise<Result<N>>;
}

export interface ProjectFileWorkspace {
	readonly id: string | null;
	readonly root: string;
	readonly trusted?: boolean;
	subscribe(listener: () => void): () => void;
}

export interface ProjectFileView {
	state: FileState;
	/** What the file preselects; empty unless it was read. */
	prefs: ProjectPrefs;
	/** Why the file is left alone, as text; null when it is not. */
	notice: string | null;
	/** The last write that failed; the choice stays for the tab. */
	writeError: Failure | null;
	writing: boolean;
}

export class ProjectFileModel {
	#client: ProjectFileClient;
	#ws: ProjectFileWorkspace;
	#state: FileState = 'idle';
	#raw: Record<string, unknown> = { schemaVersion: SCHEMA_VERSION };
	#notice: string | null = null;
	#writeError: Failure | null = null;
	/** Changes not on disk yet: made before the read answered, in flight, or whose write failed. */
	#queue: ProjectPatch[] = [];
	/** Settled when the write that carries a change ends, or when the file is left alone. */
	#waiters: (() => void)[] = [];
	#writing = false;
	#dirty = false;
	#loadedFor: string | null = null;
	#trusted = false;
	#gen = 0;
	#listeners = new Set<() => void>();
	#view: ProjectFileView | null = null;
	#off: () => void;

	constructor(client: ProjectFileClient, ws: ProjectFileWorkspace) {
		this.#client = client;
		this.#ws = ws;
		this.#off = ws.subscribe(() => this.#workspaceChanged());
		this.#workspaceChanged();
	}

	get view(): ProjectFileView {
		this.#view ??= {
			state: this.#state,
			prefs: this.#state === 'loaded' || this.#state === 'absent' ? prefsOf(this.#raw) : {},
			notice: this.#notice,
			writeError: this.#writeError,
			writing: this.#writing,
		};
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
		this.#gen++;
		this.#settle(this.#waiters.splice(0));
	}

	/** The socket (re)opened: a read the connection cut short is made again. */
	onConnection(state: string): void {
		if (state === 'open' && this.#state === 'idle') this.#workspaceChanged();
	}

	/**
	 * The user changed a choice: it is written, unless the file is one we must not touch.
	 * The promise settles when the write that carries it ends (failed or not), and at once
	 * when the file is left alone (`view.notice` says why).
	 */
	update(patch: ProjectPatch): Promise<void> {
		if (BLOCKED.includes(this.#state)) return Promise.resolve();
		this.#queue.push(patch);
		const done = new Promise<void>((resolve) => this.#waiters.push(resolve));
		if (this.#state !== 'idle' && this.#state !== 'loading') this.#flush();
		return done;
	}

	#settle(waiters: (() => void)[]): void {
		for (const w of waiters) w();
	}

	#workspaceChanged(): void {
		const id = this.#ws.id;
		const granted = this.#ws.trusted === true && !this.#trusted;
		this.#trusted = this.#ws.trusted === true;
		if (id === null) return;
		if (id !== this.#loadedFor || (granted && this.#state === 'unreadable')) void this.#load(id);
	}

	/** One read of the project file: its parsed content and revision, or why it is not ours. */
	async #read(): Promise<ReadOutcome> {
		try {
			const r = await this.#client.call('file.read', { path: PROJECT_FILE, limit: READ_LIMIT }, { workspace: this.#ws.root.trim() });
			// A page that is not the whole text (binary, cut, or more to read) is not our file.
			const whole = r.text !== undefined && r.binary !== true && r.truncated !== true && r.nextOffset >= r.size;
			const parsed = whole ? parseProjectFile(r.text ?? '') : ({ kind: 'invalid' } as Parsed);
			if (parsed.kind === 'ok') return { kind: 'ok', raw: parsed.raw, revision: r.revision };
			if (parsed.kind === 'newer') {
				return {
					kind: 'newer',
					notice: `${PROJECT_FILE} has schemaVersion ${parsed.version}, newer than this UI reads (${SCHEMA_VERSION}). It is ignored and will not be changed; your choices last for this tab only.`,
				};
			}
			return { kind: 'invalid', notice: `${PROJECT_FILE} could not be parsed. It is ignored and will not be overwritten; your choices last for this tab only.` };
		} catch (e) {
			if (e instanceof CapabilityError && e.code === 'FILE_NOT_FOUND') return { kind: 'absent' };
			if (e instanceof CapabilityError) {
				const f = failure(e);
				return {
					kind: 'unreadable',
					notice: `${PROJECT_FILE} could not be read (${f.code ?? 'error'}). It is ignored and will not be overwritten; your choices last for this tab only.`,
				};
			}
			throw e; // a lost connection says nothing about the file
		}
	}

	async #load(id: string): Promise<void> {
		const gen = ++this.#gen;
		this.#loadedFor = id;
		this.#state = 'loading';
		this.#notice = null;
		this.#raw = { schemaVersion: SCHEMA_VERSION };
		this.#changed();
		let out: ReadOutcome;
		try {
			out = await this.#read();
		} catch {
			if (gen !== this.#gen) return;
			// Read again when the socket reopens.
			this.#state = 'idle';
			this.#loadedFor = null;
			this.#changed();
			return;
		}
		if (gen !== this.#gen) return;
		if (out.kind === 'ok') {
			this.#raw = out.raw;
			this.#state = 'loaded';
		} else if (out.kind === 'absent') {
			this.#state = 'absent';
		} else {
			this.#state = out.kind;
			this.#notice = out.notice;
		}
		this.#changed();
		if (BLOCKED.includes(this.#state)) {
			this.#queue = [];
			this.#settle(this.#waiters.splice(0));
		} else if (this.#queue.length > 0) {
			this.#flush();
		}
	}

	/** One write at a time; a change made while one is in flight is written after it. */
	#flush(): void {
		if (this.#writing) {
			this.#dirty = true;
			return;
		}
		void this.#write();
	}

	async #write(): Promise<void> {
		this.#writing = true;
		this.#changed();
		const id = this.#loadedFor;
		try {
			do {
				this.#dirty = false;
				const batch = this.#queue.splice(0);
				const waiters = this.#waiters.splice(0);
				if (batch.length > 0) await this.#writeBatch(batch, id);
				this.#settle(waiters);
			} while (this.#dirty && id === this.#loadedFor && !BLOCKED.includes(this.#state));
		} finally {
			this.#writing = false;
			if (BLOCKED.includes(this.#state)) this.#settle(this.#waiters.splice(0));
			this.#changed();
		}
	}

	/**
	 * Reads the file again, applies the batch to what was read and writes it with the revision
	 * read (ADR-0075 §7). A conflict reads and patches again, three tries in all. A file
	 * that is now one we must not touch is left alone; a failure keeps the batch for the
	 * next write and shows the failure.
	 */
	async #writeBatch(batch: ProjectPatch[], id: string | null): Promise<void> {
		const fail = (e: unknown) => {
			this.#writeError = failure(e);
			this.#queue.unshift(...batch);
		};
		for (let attempt = 1; attempt <= WRITE_TRIES; attempt++) {
			let out: ReadOutcome;
			try {
				out = await this.#read();
			} catch (e) {
				fail(e);
				return;
			}
			if (id !== this.#loadedFor) return;
			if (out.kind === 'newer' || out.kind === 'invalid' || out.kind === 'unreadable') {
				this.#state = out.kind;
				this.#notice = out.notice;
				this.#queue = [];
				return;
			}
			const base = out.kind === 'ok' ? out.raw : { schemaVersion: SCHEMA_VERSION };
			const next = batch.reduce(applyPatch, base);
			if (out.kind === 'ok' && render(next) === render(base)) {
				this.#raw = base;
				this.#state = 'loaded';
				this.#writeError = null;
				return;
			}
			try {
				const ifRevision = out.kind === 'ok' ? out.revision : 'absent';
				await this.#client.call(
					'file.write',
					{ path: PROJECT_FILE, content: render(next), ...(ifRevision !== undefined && { ifRevision }) },
					{ workspace: this.#ws.root.trim() },
				);
				this.#raw = next;
				this.#state = 'loaded';
				this.#writeError = null;
				return;
			} catch (e) {
				if (e instanceof CapabilityError && e.code === 'FILE_REVISION_CONFLICT' && attempt < WRITE_TRIES) continue;
				fail(e);
				return;
			}
		}
	}

	#changed(): void {
		this.#view = null;
		for (const l of [...this.#listeners]) l();
	}
}
