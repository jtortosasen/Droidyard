// The project file (ADR-0046): `<root>/.droidyard/project.json` keeps the UI's choices for
// a project (module and variant, the AVD or serial, the application id), so nothing is
// typed again in a new tab. It is read and written by the UI through `file.read` and
// `file.write`; no capability interprets it.
//
// Rules this module keeps, each tested:
//   - the file only preselects: a value the daemon's lists lack is ignored by the models
//     that apply it, and nothing here carries trust, policy, admission or tool keys;
//   - it is read once, when the workspace opens, and never written on load: the first
//     write is the first change the user makes (D19);
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
	return next;
}

export const render = (raw: Record<string, unknown>): string => JSON.stringify(raw, null, 2) + '\n';

function mergePatch(a: ProjectPatch | null, b: ProjectPatch): ProjectPatch {
	return { ...a, ...b };
}

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
	/** Changes made before the read answered: written once it did, if the file may be. */
	#pending: ProjectPatch | null = null;
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
	}

	/** The socket (re)opened: a read the connection cut short is made again. */
	onConnection(state: string): void {
		if (state === 'open' && this.#state === 'idle') this.#workspaceChanged();
	}

	/** The user changed a choice: it is written, unless the file is one we must not touch. */
	update(patch: ProjectPatch): void {
		if (this.#state === 'idle' || this.#state === 'loading') {
			this.#pending = mergePatch(this.#pending, patch);
			return;
		}
		if (BLOCKED.includes(this.#state)) return;
		this.#apply(patch);
	}

	#workspaceChanged(): void {
		const id = this.#ws.id;
		const granted = this.#ws.trusted === true && !this.#trusted;
		this.#trusted = this.#ws.trusted === true;
		if (id === null) return;
		if (id !== this.#loadedFor || (granted && this.#state === 'unreadable')) void this.#load(id);
	}

	async #load(id: string): Promise<void> {
		const gen = ++this.#gen;
		this.#loadedFor = id;
		this.#state = 'loading';
		this.#notice = null;
		this.#raw = { schemaVersion: SCHEMA_VERSION };
		this.#changed();
		let state: FileState;
		try {
			const r = await this.#client.call('file.read', { path: PROJECT_FILE, limit: READ_LIMIT }, { workspace: this.#ws.root.trim() });
			if (gen !== this.#gen) return;
			// A page that is not the whole text (binary, cut, or more to read) is not our file.
			const whole = r.text !== undefined && r.binary !== true && r.truncated !== true && r.nextOffset >= r.size;
			const parsed = whole ? parseProjectFile(r.text ?? '') : ({ kind: 'invalid' } as Parsed);
			if (parsed.kind === 'ok') {
				this.#raw = parsed.raw;
				state = 'loaded';
			} else if (parsed.kind === 'newer') {
				state = 'newer';
				this.#notice = `${PROJECT_FILE} has schemaVersion ${parsed.version}, newer than this UI reads (${SCHEMA_VERSION}). It is ignored and will not be changed; your choices last for this tab only.`;
			} else {
				state = 'invalid';
				this.#notice = `${PROJECT_FILE} could not be parsed. It is ignored and will not be overwritten; your choices last for this tab only.`;
			}
		} catch (e) {
			if (gen !== this.#gen) return;
			if (e instanceof CapabilityError && e.code === 'FILE_NOT_FOUND') {
				state = 'absent';
			} else if (e instanceof CapabilityError) {
				state = 'unreadable';
				const f = failure(e);
				this.#notice = `${PROJECT_FILE} could not be read (${f.code ?? 'error'}). It is ignored and will not be overwritten; your choices last for this tab only.`;
			} else {
				// A lost connection says nothing about the file: read again when the socket reopens.
				this.#state = 'idle';
				this.#loadedFor = null;
				this.#changed();
				return;
			}
		}
		this.#state = state;
		const pending = this.#pending;
		this.#pending = null;
		this.#changed();
		if (pending !== null && !BLOCKED.includes(state)) this.#apply(pending);
	}

	#apply(patch: ProjectPatch): void {
		const next = applyPatch(this.#raw, patch);
		const unchanged = this.#state === 'loaded' && render(next) === render(this.#raw);
		this.#raw = next;
		if (unchanged && this.#writeError === null) return;
		this.#state = 'loaded';
		this.#flush();
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
				const content = render(this.#raw);
				try {
					await this.#client.call('file.write', { path: PROJECT_FILE, content }, { workspace: this.#ws.root.trim() });
					this.#writeError = null;
				} catch (e) {
					// The choice stays in the models; the next change writes the whole file again.
					this.#writeError = failure(e);
				}
			} while (this.#dirty && id === this.#loadedFor);
		} finally {
			this.#writing = false;
			this.#changed();
		}
	}

	#changed(): void {
		this.#view = null;
		for (const l of [...this.#listeners]) l();
	}
}
