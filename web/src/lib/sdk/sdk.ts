// The SDK panel's model (plan m3f step 8, ADR-0129, ADR-0130). It reads `sdk.status`,
// `sdk.packages` and `sdk.licenses` at open, when a terminal `sdk.*` operation of any
// client ends (the mirror's triggers: an agent's install shows here) and on Refresh. The
// install plan is computed here from the lists: the selected ids and the dependencies
// they would pull in, their sizes, which are updates, and the licences among them not yet
// accepted (ADR-0015 §6: accepting is the person's act, one licence and hash at a time).
// Everything from the repository (names, licence texts) and from the host (what uses a
// package) is untrusted text: the view carries strings, the component renders text nodes.
import type { Params, Result } from '../api/capabilities.gen';
import { CapabilityError, type CallOptions } from '../api/client';
import { failure, type Failure } from '../build/failure';
import type { Operation } from '../ops/activity';

export const SDK_TRIGGERS = ['sdk.install', 'sdk.uninstall', 'sdk.acceptLicense'] as const;
const SDK_RUNNING = ['sdk.install', 'sdk.uninstall'] as const;

type Calls =
	| 'sdk.status'
	| 'sdk.packages'
	| 'sdk.licenses'
	| 'sdk.acceptLicense'
	| 'sdk.install'
	| 'sdk.uninstall'
	| 'operation.cancel';

export interface SdkClient {
	call<N extends Calls>(name: N, params: Params<N>, opts?: CallOptions): Promise<Result<N>>;
}

export interface SdkActivity {
	active(predicate: (op: Operation) => boolean): Operation[];
	subscribe(listener: () => void): () => void;
	onTerminal(capabilities: readonly string[], fn: (op: Operation) => void): () => void;
}

type Packages = Result<'sdk.packages'>;
export type Category = Packages['available'][number]['category'];

/** The categories in the order the panel shows them. */
export const CATEGORIES: readonly { id: Category; label: string }[] = [
	{ id: 'platformTools', label: 'Platform tools' },
	{ id: 'cmdlineTools', label: 'Command-line tools' },
	{ id: 'emulator', label: 'Emulator' },
	{ id: 'platform', label: 'Platforms' },
	{ id: 'buildTools', label: 'Build tools' },
	{ id: 'systemImage', label: 'System images' },
	{ id: 'other', label: 'Other' },
];

export interface SdkRow {
	id: string;
	category: Category;
	name: string;
	installed: boolean;
	/** The installed revision. */
	version: string | undefined;
	/** The newer revision on offer, for an installed row. */
	update: string | undefined;
	/** The revision to install, for a row that is not installed. */
	available: string | undefined;
	/** Bytes to download when installed or updated. */
	size: number | undefined;
	sizeOnDisk: number | undefined;
	/** Install (not installed) or Update (an `update`) is possible. */
	selectable: boolean;
}

export interface SdkGroup {
	category: Category;
	label: string;
	rows: SdkRow[];
}

export interface PendingLicence {
	id: string;
	sha1: string;
	/** The packages of the plan under it. */
	packages: string[];
	/** The text from `sdk.licenses`; absent when the list does not hold it. */
	text: string | undefined;
}

export interface PlanItem {
	id: string;
	size: number;
	update: boolean;
	/** Pulled in by a dependency of a selected package. */
	dependency: boolean;
}

export interface InstallPlan {
	items: PlanItem[];
	totalSize: number;
	pending: PendingLicence[];
}

export interface InUse {
	id: string;
	usedBy: string[];
}

export interface SdkView {
	loaded: boolean;
	found: boolean;
	root: string | undefined;
	/** The root a first install would set up, from `sdk.licenses`, when there is no SDK. */
	targetRoot: string | undefined;
	warnings: readonly string[];
	repository: Packages['repository'] | undefined;
	truncated: boolean;
	groups: SdkGroup[];
	selected: readonly string[];
	plan: InstallPlan;
	/** The install confirm is open. */
	confirming: boolean;
	/** The installed id whose uninstall confirm is open. */
	uninstalling: { id: string; sizeOnDisk: number | undefined } | null;
	/** True once the plan has packages and no licence is pending: the Install action is enabled. */
	canRun: boolean;
	/** A call of this tab is in flight, or any client's sdk.install / sdk.uninstall runs. */
	busy: boolean;
	op: { operationId: string; capability: string; client: string | undefined; state: 'queued' | 'running' } | null;
	error: Failure | null;
	inUse: readonly InUse[];
	notice: string | null;
	loadError: Failure | null;
}

export class SdkModel {
	#client: SdkClient;
	#activity: SdkActivity;
	#listeners = new Set<() => void>();
	#view: SdkView | null = null;
	#offs: (() => void)[] = [];
	#gen = 0;
	#loaded = false;
	#status: Result<'sdk.status'> | null = null;
	#pkgs: Packages | null = null;
	#licences: Result<'sdk.licenses'> | null = null;
	#selected: string[] = [];
	#confirming = false;
	#uninstalling: SdkView['uninstalling'] = null;
	#calling = false;
	#error: Failure | null = null;
	#inUse: InUse[] = [];
	#notice: string | null = null;
	#loadError: Failure | null = null;

	constructor(client: SdkClient, activity: SdkActivity) {
		this.#client = client;
		this.#activity = activity;
		this.#offs.push(activity.subscribe(() => this.#changed()));
		this.#offs.push(activity.onTerminal(SDK_TRIGGERS, () => void this.refresh()));
	}

	get view(): SdkView {
		if (this.#view === null) {
			const groups = buildGroups(this.#pkgs);
			const rows = groups.flatMap((g) => g.rows);
			// A selection that no longer names a selectable row (installed meanwhile) is dropped from the plan.
			const selected = this.#selected.filter((id) => rows.some((r) => r.id === id && r.selectable));
			const plan = buildPlan(selected, this.#pkgs, this.#licences);
			const op = this.#running();
			const busy = this.#calling || op !== null;
			this.#view = {
				loaded: this.#loaded,
				found: this.#status?.found ?? this.#pkgs?.found ?? false,
				root: this.#status?.root ?? this.#pkgs?.root,
				targetRoot: this.#licences?.root,
				warnings: [...(this.#status?.warnings ?? []), ...(this.#pkgs?.warnings ?? [])],
				repository: this.#pkgs?.repository,
				truncated: this.#pkgs?.truncated ?? false,
				groups,
				selected,
				plan,
				confirming: this.#confirming,
				uninstalling: this.#uninstalling,
				canRun: plan.items.length > 0 && plan.pending.length === 0 && !busy,
				busy,
				op,
				error: this.#error,
				inUse: this.#inUse,
				notice: this.#notice,
				loadError: this.#loadError,
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

	/** Reads the three lists; a newer read supersedes an older one on its way. */
	async refresh(refetch = false): Promise<void> {
		const gen = ++this.#gen;
		let loadError: Failure | null = null;
		const read = async <T>(f: () => Promise<T>): Promise<T | null> => {
			try {
				return await f();
			} catch (e) {
				// A lost connection is the connection state's to show; a capability error has a code.
				if (e instanceof CapabilityError) loadError ??= failure(e);
				return null;
			}
		};
		const [status, pkgs, licences] = await Promise.all([
			read(() => this.#client.call('sdk.status', {})),
			read(() => this.#client.call('sdk.packages', refetch ? { refresh: true } : {})),
			read(() => this.#client.call('sdk.licenses', {})),
		]);
		if (gen !== this.#gen) return;
		if (status) this.#status = status;
		if (pkgs) this.#pkgs = pkgs;
		if (licences) this.#licences = licences;
		this.#loaded = true;
		this.#loadError = loadError;
		this.#changed();
	}

	toggle(id: string): void {
		this.#selected = this.#selected.includes(id) ? this.#selected.filter((s) => s !== id) : [...this.#selected, id];
		this.#changed();
	}

	/** Update on a row with `update`: that package alone, straight to the confirm. */
	update(id: string): void {
		this.#selected = [id];
		this.#view = null;
		this.openConfirm();
	}

	/** Set up: `platform-tools` into the target root (ADR-0130 §4: `sdk.install` creates the root). */
	setUp(): void {
		this.#selected = ['platform-tools'];
		this.#view = null;
		this.openConfirm();
	}

	openConfirm(): void {
		if (this.view.plan.items.length === 0) return;
		this.#confirming = true;
		this.#error = null;
		this.#inUse = [];
		this.#changed();
	}

	closeConfirm(): void {
		this.#confirming = false;
		this.#changed();
	}

	openUninstall(id: string): void {
		const row = this.view.groups.flatMap((g) => g.rows).find((r) => r.id === id && r.installed);
		if (row === undefined) return;
		this.#uninstalling = { id, sizeOnDisk: row.sizeOnDisk };
		this.#error = null;
		this.#inUse = [];
		this.#changed();
	}

	closeUninstall(): void {
		this.#uninstalling = null;
		this.#changed();
	}

	/** The person's Accept of one licence text: `sdk.acceptLicense` with that id and hash, then the lists again. */
	async accept(id: string, sha1: string): Promise<void> {
		this.#error = null;
		try {
			await this.#client.call('sdk.acceptLicense', { id, sha1 });
		} catch (e) {
			this.#error = failure(e);
			this.#changed();
			return;
		}
		await this.refresh();
	}

	/** The confirm's Install: refused while a licence is pending or a call runs. */
	async run(): Promise<void> {
		const v = this.view;
		if (!v.canRun) return;
		const packages = v.selected as unknown as Params<'sdk.install'>['packages'];
		await this.#call(async () => {
			const r = await this.#client.call('sdk.install', { packages });
			this.#notice = `Installed ${r.installed.length}, updated ${r.updated.length}.` + (r.warnings.length > 0 ? ` ${r.warnings.join(' ')}` : '');
			this.#selected = [];
			this.#confirming = false;
		});
	}

	async uninstall(): Promise<void> {
		const u = this.#uninstalling;
		if (u === null || this.view.busy) return;
		await this.#call(async () => {
			const r = await this.#client.call('sdk.uninstall', { packages: [u.id] as unknown as Params<'sdk.uninstall'>['packages'] });
			this.#notice = `Removed ${r.removed.join(', ')}.` + (r.warnings.length > 0 ? ` ${r.warnings.join(' ')}` : '');
			this.#uninstalling = null;
		});
	}

	/** Cancels the install or uninstall shown, whoever started it (any session may cancel). */
	async cancel(operationId: string): Promise<void> {
		if (this.view.op?.operationId !== operationId) return;
		try {
			await this.#client.call('operation.cancel', { operationId });
		} catch (e) {
			this.#error = failure(e);
			this.#changed();
		}
	}

	dispose(): void {
		for (const off of this.#offs) off();
		this.#offs = [];
	}

	async #call(f: () => Promise<void>): Promise<void> {
		this.#calling = true;
		this.#error = null;
		this.#inUse = [];
		this.#notice = null;
		this.#changed();
		try {
			await f();
		} catch (e) {
			this.#error = failure(e);
			if (e instanceof CapabilityError && e.code === 'SDK_PACKAGE_IN_USE') this.#inUse = inUseOf(e.details);
		}
		this.#calling = false;
		this.#changed();
		// The call changed the lists whatever the client; a refused one may have changed a licence.
		void this.refresh();
	}

	#running(): SdkView['op'] {
		const op = this.#activity
			.active((o) => (SDK_RUNNING as readonly string[]).includes(o.capability))
			.sort((a, b) => (a.state === b.state ? 0 : a.state === 'running' ? -1 : 1))[0];
		if (op === undefined || (op.state !== 'queued' && op.state !== 'running')) return null;
		return { operationId: op.id, capability: op.capability, client: op.client, state: op.state };
	}

	#changed(): void {
		this.#view = null;
		for (const l of [...this.#listeners]) l();
	}
}

/** `details.packages` of SDK_PACKAGE_IN_USE: each package and what uses it. Host text: shown as text. */
export function inUseOf(details: Record<string, unknown> | undefined): InUse[] {
	const list = details?.packages;
	if (!Array.isArray(list)) return [];
	const out: InUse[] = [];
	for (const p of list) {
		if (typeof p !== 'object' || p === null) continue;
		const { id, usedBy } = p as { id?: unknown; usedBy?: unknown };
		if (typeof id !== 'string') continue;
		out.push({ id, usedBy: Array.isArray(usedBy) ? usedBy.filter((u): u is string => typeof u === 'string') : [] });
	}
	return out;
}

/** The rows, grouped by category: installed packages first (with their update), then the others on offer. */
export function buildGroups(pkgs: Packages | null): SdkGroup[] {
	if (pkgs === null) return [];
	const avail = new Map(pkgs.available.map((a) => [a.id, a]));
	const rows: SdkRow[] = [];
	for (const i of pkgs.installed) {
		const a = avail.get(i.id);
		rows.push({
			id: i.id,
			category: a?.category ?? 'other',
			name: a?.displayName ?? i.id,
			installed: true,
			version: i.version,
			update: i.update,
			available: a?.version,
			size: i.update === undefined ? undefined : a?.size,
			sizeOnDisk: i.sizeOnDisk,
			selectable: i.update !== undefined && a !== undefined,
		});
	}
	const have = new Set(pkgs.installed.map((i) => i.id));
	for (const a of pkgs.available) {
		if (have.has(a.id)) continue;
		rows.push({
			id: a.id,
			category: a.category,
			name: a.displayName,
			installed: false,
			version: undefined,
			update: undefined,
			available: a.version,
			size: a.size,
			sizeOnDisk: undefined,
			selectable: true,
		});
	}
	return CATEGORIES.map((c) => ({ category: c.id, label: c.label, rows: rows.filter((r) => r.category === c.id) })).filter((g) => g.rows.length > 0);
}

/**
 * What an Install would do: the selected ids and, transitively, the dependencies of them that
 * are not installed; the sizes to download; and each licence (id and hash) of those packages
 * that is not accepted, with the packages under it and its text. A dependency's licence counts
 * (ADR-0015 §6): the daemon refuses an install whose dependency is under an unaccepted licence.
 */
export function buildPlan(selected: readonly string[], pkgs: Packages | null, licences: Result<'sdk.licenses'> | null): InstallPlan {
	const empty: InstallPlan = { items: [], totalSize: 0, pending: [] };
	if (pkgs === null) return empty;
	const avail = new Map(pkgs.available.map((a) => [a.id, a]));
	const installed = new Map(pkgs.installed.map((i) => [i.id, i]));
	const items: PlanItem[] = [];
	const seen = new Set<string>();
	const visit = (id: string, dependency: boolean) => {
		if (seen.has(id)) return;
		seen.add(id);
		const a = avail.get(id);
		if (a === undefined) return;
		items.push({ id, size: a.size, update: installed.get(id)?.update !== undefined, dependency });
		for (const d of a.dependencies) if (!installed.has(d.id)) visit(d.id, true);
	};
	for (const id of selected) visit(id, false);
	const pending: PendingLicence[] = [];
	for (const it of items) {
		const a = avail.get(it.id)!;
		if (a.licenseAccepted) continue;
		let p = pending.find((x) => x.id === a.license.id && x.sha1 === a.license.sha1);
		if (p === undefined) {
			p = {
				id: a.license.id,
				sha1: a.license.sha1,
				packages: [],
				text: licences?.licenses.find((l) => l.id === a.license.id && l.sha1 === a.license.sha1)?.text,
			};
			pending.push(p);
		}
		p.packages.push(it.id);
	}
	return { items, totalSize: items.reduce((n, i) => n + i.size, 0), pending };
}

/** Bytes as a short size: 1.5 MB. */
export function sizeText(n: number | undefined): string {
	if (n === undefined) return 'unknown size';
	const units = ['B', 'KB', 'MB', 'GB'];
	let v = n;
	let u = 0;
	while (v >= 1024 && u < units.length - 1) {
		v /= 1024;
		u++;
	}
	return `${u === 0 ? v : v.toFixed(1)} ${units[u]}`;
}
