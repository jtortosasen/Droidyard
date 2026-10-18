// Android commands an agent ran outside droidyard (ADR-0063 §6, plan m2k step 8). The hook
// records each through `agent.recordCommand`, whose result is the record; the daemon keeps
// it only as the operation's result. Activity reads a completed row's result once
// (`operation.wait`, timeoutMs 0, in the wire shape restore.ts documents: the envelope's
// `result` is the waited operation's own result) and caches the answer, a failed read
// included, so a row costs one read. A failed or malformed read leaves the default title.
import type { Params, Result } from '../api/capabilities.gen';
import { inert } from '../devices/text';

export const RECORD_COMMAND = 'agent.recordCommand';
/** The hook's record of files an agent's own edit tool wrote (ADR-0095 §3); read like a command record. */
export const RECORD_EDIT = 'agent.recordEdit';
/** The hook's question before an edit: the daemon's own plumbing, never shown. */
export const CHECK_WRITE = 'agent.checkWrite';

/** Whether the capability is one of the hook's records. */
export function isRecord(capability: string): boolean {
	return capability === RECORD_COMMAND || capability === RECORD_EDIT;
}

export interface CommandsClient {
	call(name: 'operation.wait', params: Params<'operation.wait'>): Promise<Result<'operation.wait'>>;
}

/** What a block shows of a record. */
export interface CommandRecord {
	/** "Claude Code" or "Cursor". */
	actor: string;
	/** `gradle assembleDebug`: the program and its words, as text. */
	command: string;
	/** The droidyard tool that does the same, when the record names one. */
	suggested?: string;
	/** The block's whole title, for a record that is not a command: "edited a.kt". */
	title?: string;
}

const ACTORS: Record<string, string> = { claude: 'Claude Code', cursor: 'Cursor' };

/** Reads the wire result defensively; null when it is not a record we understand. */
export function parseRecord(raw: unknown): CommandRecord | null {
	const res = raw as Partial<Result<'agent.recordCommand'>> | null | undefined;
	if (res === null || typeof res !== 'object') return null;
	const actor = typeof res.client === 'string' ? ACTORS[res.client] : undefined;
	const first = Array.isArray(res.commands) ? res.commands[0] : undefined;
	if (actor === undefined || first === undefined || typeof first.program !== 'string') return null;
	const words = Array.isArray(first.words) ? first.words.filter((w): w is string => typeof w === 'string') : [];
	const more = (res.commands?.length ?? 1) - 1;
	return {
		actor,
		command: [first.program, ...words].join(' ') + (more > 0 ? ` (+${more} more)` : ''),
		suggested: typeof first.suggested === 'string' && first.suggested !== '' ? first.suggested : undefined,
	};
}

/** Reads an `agent.recordEdit` result: "edited|created <path>", "(+n more)", and "(outside the project)". */
export function parseEditRecord(raw: unknown): CommandRecord | null {
	const res = raw as Partial<Result<'agent.recordEdit'>> | null | undefined;
	if (res === null || typeof res !== 'object') return null;
	const actor = typeof res.client === 'string' ? ACTORS[res.client] : undefined;
	const first = Array.isArray(res.files) ? res.files[0] : undefined;
	if (actor === undefined || first === undefined || typeof first.path !== 'string') return null;
	const more = (res.files?.length ?? 1) - 1;
	const path = inert(first.path);
	const text = path + (more > 0 ? ` (+${more} more)` : '') + (res.outside === true ? ' (outside the project)' : '');
	return { actor, command: text, title: `${first.created === true ? 'created' : 'edited'} ${text}` };
}

export class CommandRecordsModel {
	#client: CommandsClient;
	// null: the read was made and gave nothing usable.
	#records = new Map<string, CommandRecord | null>();
	#listeners = new Set<() => void>();

	constructor(client: CommandsClient) {
		this.#client = client;
	}

	recordOf(operationId: string): CommandRecord | undefined {
		return this.#records.get(operationId) ?? undefined;
	}

	subscribe(listener: () => void): () => void {
		this.#listeners.add(listener);
		return () => {
			this.#listeners.delete(listener);
		};
	}

	/** Reads the row's result the first time it is asked; later calls do nothing. Never throws. */
	async load(operationId: string, capability: string = RECORD_COMMAND): Promise<void> {
		if (this.#records.has(operationId)) return;
		this.#records.set(operationId, null); // claimed: one read per row, whatever comes back
		try {
			const waited: unknown = await this.#client.call('operation.wait', { operationId, timeoutMs: 0 });
			const rec = capability === RECORD_EDIT ? parseEditRecord(waited) : parseRecord(waited);
			if (rec === null) return;
			this.#records.set(operationId, rec);
			for (const l of [...this.#listeners]) l();
		} catch {
			// The default title stays.
		}
	}
}
