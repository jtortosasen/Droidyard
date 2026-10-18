// The UI tree of the selected device (plan m2b step 6): `device.uiTree {device}`, nodes
// indented by `children`, each row its class, id, text and desc. It is read when the user
// asks and when an operation of TREE_TRIGGERS ends (a tap, a typed text, a gesture in the live view, a launch or an
// install, by any client) while a tree is shown; never polled, and a read of its own is not
// a trigger. Picking a node gives the selector the action form is filled with. Every
// device field leaves here as a string for a text node (see text.ts); the raw node stays
// in the model for the selector, which must match the device's text exactly.
import type { Params, Result } from '../api/capabilities.gen';
import { CapabilityError, type CallOptions } from '../api/client';
import { failure, type Failure } from '../build/failure';
import type { Operation } from '../ops/activity';
import type { DevicesActivity } from './mirror';
import type { DeviceSelection } from './install';
import { inert, quoted } from './text';

export type UiTree = Result<'device.uiTree'>;
export type UiNode = UiTree['nodes'][number];

/** The selector forms a pick produces: all inside the vocabulary of device.tapElement. */
export type Selector = { id: string } | { text: string } | { ref: string; snapshot: string };

/**
 * Plan step 6: `id` when the node has one, else `text`, else `{ref, snapshot}` (the snapshot
 * the tree came from; the daemon refuses a stale one with DEVICE_STALE_REF).
 */
export function selectorFor(node: UiNode, snapshot: string): Selector {
	if (node.id !== undefined && node.id !== '') return { id: node.id };
	if (node.text !== undefined && node.text !== '') return { text: node.text };
	return { ref: node.ref, snapshot };
}

export interface TreeRow {
	/** Position in `nodes`; the only key a row needs (refs are the device's). */
	index: number;
	/** Two spaces per level, as text. */
	indent: string;
	class: string;
	id: string;
	text: string;
	desc: string;
	flags: string;
}

const MAX_INDENT = 40;

/** The nodes in document order, depth from `children`; a ref seen twice or a cycle is shown once. */
export function treeRows(nodes: readonly UiNode[]): TreeRow[] {
	const first = new Map<string, number>();
	nodes.forEach((n, i) => {
		if (!first.has(n.ref)) first.set(n.ref, i);
	});
	const isChild = new Set<number>();
	for (const n of nodes) for (const c of n.children ?? []) {
		const i = first.get(c);
		if (i !== undefined) isChild.add(i);
	}
	const rows: TreeRow[] = [];
	const seen = new Set<number>();
	const visit = (root: number) => {
		const stack: [number, number][] = [[root, 0]];
		while (stack.length > 0) {
			const [i, depth] = stack.pop()!;
			if (seen.has(i)) continue;
			seen.add(i);
			const n = nodes[i]!;
			rows.push({
				index: i,
				indent: '  '.repeat(Math.min(depth, MAX_INDENT)),
				class: inert(n.class ?? ''),
				id: inert(n.id ?? ''),
				text: inert(n.text ?? ''),
				desc: inert(n.desc ?? ''),
				flags: inert((n.flags ?? []).join(' ')),
			});
			const kids = (n.children ?? []).map((c) => first.get(c)).filter((c): c is number => c !== undefined);
			for (let k = kids.length - 1; k >= 0; k--) stack.push([kids[k]!, depth + 1]);
		}
	};
	nodes.forEach((_, i) => {
		if (!isChild.has(i)) visit(i);
	});
	// Whatever a cycle kept out of every root.
	nodes.forEach((_, i) => visit(i));
	return rows;
}

export interface UiTreeClient {
	call(name: 'device.uiTree', params: Params<'device.uiTree'>, opts?: CallOptions): Promise<Result<'device.uiTree'>>;
}

export interface UiTreeView {
	/** The selected device serial; '' for none. */
	device: string;
	/** A tree is shown (read at least once for this device). */
	loaded: boolean;
	loading: boolean;
	rows: readonly TreeRow[];
	/** One line of text each: foreground, display, truncation, hint, system dialog, warnings. */
	notes: readonly string[];
	error: Failure | null;
	/** The inspected row (an index into the rows' `index`); null for none. */
	selected: number | null;
	/** The inspected node with the snapshot it came from, raw: the view renders it as text. */
	selection: { node: UiNode; snapshot: string } | null;
}

/** The last segment of a class name (`android.widget.Button` shows as `Button`), as text. */
export function shortClass(cls: string): string {
	const i = cls.lastIndexOf('.');
	return i < 0 ? cls : cls.slice(i + 1);
}

/** What a tap, a typed text, a launch or an install changes on the screen. */
export const UI_TRIGGERS = ['device.tapElement', 'device.inputText', 'device.launch', 'device.install'] as const;

/**
 * Whether a terminal operation may have changed `device`'s screen (ADR-0076): it is on that
 * device, or names no device (a capability that declares no target, so it mirrors as it did
 * before the target existed).
 */
export function concernsDevice(op: Pick<Operation, 'target'>, device: string): boolean {
	const named = op.target?.device;
	return named === undefined || named === '' || named === device;
}

/** The tree's own list: those, and the live view's three inputs (plan m2p step 7). App logs keep UI_TRIGGERS, so a gesture costs no `device.logs` read. */
export const TREE_TRIGGERS = [...UI_TRIGGERS, 'device.tap', 'device.swipe', 'device.pressKey'] as const;

export class UiTreeModel {
	#client: UiTreeClient;
	#devices: DeviceSelection;
	#listeners = new Set<() => void>();
	#view: UiTreeView | null = null;
	#offs: (() => void)[] = [];
	#tree: UiTree | null = null;
	/** The device the tree (or the read in flight) belongs to. */
	#for = '';
	#loading = false;
	#error: Failure | null = null;
	#gen = 0;
	/** The inspected node's index in the tree shown; a node belongs to its snapshot, so a new tree clears it. */
	#selected: number | null = null;

	constructor(client: UiTreeClient, devices: DeviceSelection, activity: Pick<DevicesActivity, 'onTerminal'>) {
		this.#client = client;
		this.#devices = devices;
		this.#offs.push(devices.subscribe(() => this.#deviceChanged()));
		// Only while a tree is shown, and only for an operation on its device or on none in
		// particular (ADR-0076): another device's tap costs no read.
		this.#offs.push(activity.onTerminal(TREE_TRIGGERS, (op) => {
			if (concernsDevice(op, this.#for)) void this.#reload();
		}));
	}

	get view(): UiTreeView {
		this.#view ??= this.#build();
		return this.#view;
	}

	subscribe(listener: () => void): () => void {
		this.#listeners.add(listener);
		return () => {
			this.#listeners.delete(listener);
		};
	}

	/** The selector for the row at `index` of the tree shown; null when it is gone. */
	selectorAt(index: number): Selector | null {
		const node = this.#tree?.nodes[index];
		return node === undefined || this.#tree === null ? null : selectorFor(node, this.#tree.snapshot);
	}

	/** Inspect the row at `index` of the tree shown; an index that is not there changes nothing. */
	select(index: number): void {
		if (this.#tree?.nodes[index] === undefined) return;
		this.#selected = index;
		this.#changed();
	}

	/** `device.uiTree` on the selected device, on the user's request. */
	async load(): Promise<void> {
		const device = this.#devices.view.selected;
		if (device === '') return;
		const gen = ++this.#gen;
		this.#for = device;
		this.#loading = true;
		this.#error = null;
		this.#changed();
		try {
			const r = await this.#client.call('device.uiTree', { device });
			if (gen !== this.#gen) return;
			this.#tree = r;
			this.#selected = null;
		} catch (e) {
			if (gen !== this.#gen) return;
			// A lost connection is shown by the connection state.
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

	/** A tree belongs to the device it was read from: another selection shows none. */
	#deviceChanged(): void {
		if (this.#for !== this.#devices.view.selected) {
			this.#gen++;
			this.#for = '';
			this.#tree = null;
			this.#selected = null;
			this.#error = null;
			this.#loading = false;
		}
		this.#changed();
	}

	#build(): UiTreeView {
		const t = this.#tree;
		const notes: string[] = [];
		const node = this.#selected === null ? undefined : t?.nodes[this.#selected];
		if (t) {
			notes.push(`Foreground: ${quoted(t.foreground)}. Display ${t.display.width}x${t.display.height}, rotation ${t.display.rotation}. ${t.nodes.length} nodes.`);
			if (t.truncated) notes.push('The tree was truncated.');
			if (t.emptyReason) notes.push(`Empty: ${inert(t.emptyReason)}`);
			if (t.hint) notes.push(`Hint: ${inert(t.hint)}`);
			if (t.systemDialog) {
				notes.push(`System dialog (${t.systemDialog.kind}) of ${quoted(t.systemDialog.package ?? 'an unknown package')}.`);
			}
			for (const w of t.warnings ?? []) notes.push(`Warning: ${inert(w)}`);
		}
		return {
			device: this.#devices.view.selected,
			loaded: t !== null || this.#error !== null,
			loading: this.#loading,
			rows: t ? treeRows(t.nodes) : [],
			notes,
			error: this.#error,
			selected: this.#selected,
			selection: node === undefined || t === null ? null : { node, snapshot: t.snapshot },
		};
	}

	#changed(): void {
		this.#view = null;
		for (const l of [...this.#listeners]) l();
	}
}
