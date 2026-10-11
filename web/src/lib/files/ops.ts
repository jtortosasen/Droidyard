// The tree's file operations over the client (plan m2g step 3; D58). Names are checked
// here, before any call; a failed call or item comes back as text for the inline field.
import type { Params, Result } from '../api/capabilities.gen';
import type { CallOptions } from '../api/client';
import { failure, failureText } from '../build/failure';

export interface OpsClient {
	call(name: 'file.create', params: Params<'file.create'>, opts?: CallOptions): Promise<Result<'file.create'>>;
	call(name: 'file.move', params: Params<'file.move'>, opts?: CallOptions): Promise<Result<'file.move'>>;
}

export interface OpsWorkspace {
	readonly root: string;
}

export type Outcome = { ok: true; path: string } | { ok: false; message: string };

/** Why a name cannot be used (D58), or null. One path segment: no `/`, no NUL, not empty, `.` or `..`. */
export function refuseName(name: string): string | null {
	if (name === '') return 'A name is required.';
	if (name === '.' || name === '..') return `"${name}" is not a name.`;
	if (name.includes('/')) return 'A name cannot contain "/".';
	if (name.includes('\0')) return 'A name cannot contain a NUL character.';
	return null;
}

function join(dir: string, name: string): string {
	return dir === '' ? name : `${dir}/${name}`;
}

export function parentOf(path: string): string {
	const i = path.lastIndexOf('/');
	return i < 0 ? '' : path.slice(0, i);
}

export function nameOf(path: string): string {
	return path.slice(path.lastIndexOf('/') + 1);
}

/** What a failed item or call says, as text; an existing name reads as "x already exists". */
function describe(code: string | undefined, message: string, name: string, nextStep?: string): string {
	if (code === 'FILE_EXISTS') return `${name} already exists`;
	return failureText({ code, message, nextStep });
}

export class FileOps {
	#client: OpsClient;
	#ws: OpsWorkspace;

	constructor(client: OpsClient, ws: OpsWorkspace) {
		this.#client = client;
		this.#ws = ws;
	}

	/** Creates `name` in the folder `dir` ('' is the root). */
	async create(dir: string, name: string, type: 'file' | 'dir'): Promise<Outcome> {
		const why = refuseName(name);
		if (why !== null) return { ok: false, message: why };
		const path = join(dir, name);
		try {
			const r = await this.#client.call('file.create', { path, type }, { workspace: this.#ws.root.trim() });
			return { ok: true, path: r.path };
		} catch (e) {
			const f = failure(e);
			return { ok: false, message: describe(f.code, f.message, name, f.nextStep) };
		}
	}

	/** Renames the entry at `path` to `name` in the same folder: a one-item `file.move`. */
	async rename(path: string, name: string): Promise<Outcome> {
		const why = refuseName(name);
		if (why !== null) return { ok: false, message: why };
		const to = join(parentOf(path), name);
		if (to === path) return { ok: true, path };
		try {
			const r = await this.#client.call('file.move', { items: [{ from: path, to }] }, { workspace: this.#ws.root.trim() });
			const item = r.items[0];
			if (item === undefined) return { ok: false, message: 'The daemon returned no result for the rename.' };
			if (item.code !== undefined) return { ok: false, message: describe(item.code, item.message ?? item.code, name) };
			return { ok: true, path: item.to };
		} catch (e) {
			const f = failure(e);
			return { ok: false, message: describe(f.code, f.message, name, f.nextStep) };
		}
	}
}
