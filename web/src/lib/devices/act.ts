// Tap and type with expectations (plan m2b step 6): `device.tapElement {device, target,
// expect, timeoutMs}` and `device.inputText {device, target?, text, clear, submit, expect,
// timeoutMs}` on the selected device. The target is filled by picking a node in the tree
// or typed (id, text, desc, class); up to MAX_EXPECT expectations (`appears` or `gone`, by
// id or text) are sent with the action and the answer is shown as `matched`, unmet with
// candidates, vacuous, the app crash and the system dialog. Nothing here retries: an
// inputText types again when repeated. Every device field in an answer leaves as a
// string for a text node (see text.ts).
import type { Params, Result } from '../api/capabilities.gen';
import { CapabilityError, type CallOptions } from '../api/client';
import { failure, type Failure } from '../build/failure';
import type { DeviceSelection } from './install';
import type { Selector } from './uitree';
import { inert, quoted } from './text';

type Calls = 'device.tapElement' | 'device.inputText';

export interface ActClient {
	call<N extends Calls>(name: N, params: Params<N>, opts?: CallOptions): Promise<Result<N>>;
}

export type Expect = NonNullable<Params<'device.tapElement'>['expect']>;
export type Target = Params<'device.tapElement'>['target'];

/** The schema's bounds (`expect` at most 5, timeoutMs at most 20000). */
export const MAX_EXPECT = 5;
export const MAX_TIMEOUT_MS = 20000;
export const MAX_TEXT = 500;

export interface ExpectRow {
	kind: 'appears' | 'gone';
	by: 'id' | 'text';
	value: string;
}

/** The expectations to send: rows with a value, the first MAX_EXPECT; undefined when none. */
export function buildExpect(rows: readonly ExpectRow[]): Expect | undefined {
	const used = rows.filter((r) => r.value !== '').slice(0, MAX_EXPECT);
	if (used.length === 0) return undefined;
	return used.map((r) => {
		const sel = r.by === 'id' ? { id: r.value } : { text: r.value };
		return r.kind === 'appears' ? { appears: sel } : { gone: sel };
	}) as Expect;
}

export interface TargetFields {
	id: string;
	text: string;
	desc: string;
	cls: string;
}

/** The typed fields as a target; a picked ref wins and stands alone (the schema forbids more). */
export function buildTarget(f: TargetFields, ref: { ref: string; snapshot: string } | null): Target | null {
	if (ref !== null) return { ref: ref.ref, snapshot: ref.snapshot };
	const t: Target = {};
	if (f.id !== '') t.id = f.id;
	if (f.text !== '') t.text = f.text;
	if (f.desc !== '') t.desc = f.desc;
	if (f.cls !== '') t.class = f.cls;
	return Object.keys(t).length === 0 ? null : t;
}

/** '' is "the default"; otherwise a whole number of milliseconds within the schema's bound. */
export function parseTimeout(s: string): { ms?: number; error?: string } {
	const t = s.trim();
	if (t === '') return {};
	const n = Number(t);
	if (!Number.isInteger(n) || n < 1 || n > MAX_TIMEOUT_MS) return { error: `timeoutMs is a whole number from 1 to ${MAX_TIMEOUT_MS}.` };
	return { ms: n };
}

type Answer = Pick<Result<'device.tapElement'>, 'expect' | 'warnings' | 'appCrash' | 'systemDialog' | 'foreground'>;

export interface ActSummary {
	/** Which action answered. */
	action: 'tap' | 'type';
	/** The expectations' verdict; null when none were sent. */
	matched: boolean | null;
	headline: string;
	lines: readonly string[];
}

type Cond = NonNullable<NonNullable<Answer['expect']>['vacuous']>[number];

function selectorText(s: Record<string, unknown>): string {
	const parts: string[] = [];
	for (const k of ['id', 'text', 'desc', 'class'] as const) {
		const v = s[k];
		if (typeof v === 'string') parts.push(`${k} ${quoted(v)}`);
	}
	if (typeof s.ref === 'string') parts.push(`ref ${quoted(s.ref)}`);
	return parts.join(', ');
}

function conditionText(c: Cond): string {
	if (c.appears) return `appears: ${selectorText(c.appears)}`;
	if (c.gone) return `gone: ${selectorText(c.gone)}`;
	if (c.foreground !== undefined) return `foreground: ${quoted(c.foreground)}`;
	return 'an empty condition';
}

/** The answer of either action, as lines of text and the verdict. */
export function summarize(action: 'tap' | 'type', r: Answer & { tapped?: Result<'device.tapElement'>['tapped']; typed?: boolean; characters?: number; method?: string }): ActSummary {
	const lines: string[] = [];
	if (r.tapped) {
		const t = r.tapped;
		const what = [t.class, t.id, t.text, t.desc].filter((x): x is string => x !== undefined && x !== '').map(quoted).join(' ');
		lines.push(`Tapped ${what === '' ? 'a node' : what} (matched ${t.matchedBy}) at ${t.point[0]},${t.point[1]}.`);
		if (!t.clickable) {
			const a = t.clickableAncestor;
			lines.push(
				`The node is not clickable, so the tap may have no effect${a ? `; its nearest clickable ancestor is ${[a.class, a.id, a.text].filter((x): x is string => x !== undefined && x !== '').map(quoted).join(' ')}` : ''}.`,
			);
		}
	}
	if (action === 'type') {
		lines.push(
			r.typed ? `Typed ${r.characters ?? 0} characters (${inert(r.method ?? 'unknown method')}).` : 'Nothing was typed.',
		);
	}
	const e = r.expect;
	let matched: boolean | null = null;
	let headline = 'Done, no expectations sent';
	if (e) {
		matched = e.matched;
		headline = e.matched ? `Matched after ${e.afterMs} ms` : `Not matched after ${e.afterMs} ms`;
		lines.push(`Expectations read ${e.polls} times, ${e.failedDumps} reads failed.`);
		if (e.reason) lines.push(`Reason: ${inert(e.reason)}`);
		for (const v of e.vacuous ?? []) lines.push(`Already true before the action, so it proves nothing: ${conditionText(v)}`);
		for (const u of e.unmet ?? []) {
			lines.push(`Unmet: ${conditionText(u.condition)}`);
			for (const c of u.candidates ?? []) {
				const what = [c.class, c.id, c.text].filter((x): x is string => x !== undefined && x !== '').map(quoted).join(' ');
				lines.push(`  candidate: ${what === '' ? 'a node' : what}`);
			}
		}
	}
	if (r.foreground) {
		lines.push(`Foreground ${quoted(r.foreground.before)}${r.foreground.after === undefined ? ', after unknown' : ` to ${quoted(r.foreground.after)}`}.`);
	}
	if (r.appCrash) {
		const c = r.appCrash;
		lines.push(`App crash (${c.source}) in ${quoted(c.package)}${c.pid === undefined ? '' : `, pid ${c.pid}`}${c.time === undefined ? '' : ` at ${inert(c.time)}`}.`);
		if (c.exception !== undefined || c.message !== undefined) {
			lines.push(`${inert(c.exception ?? '')}${c.message === undefined ? '' : `: ${inert(c.message)}`}`);
		}
		if (c.reason !== undefined) lines.push(`Exit reason: ${inert(c.reason)}`);
		for (const f of c.frames ?? []) lines.push(`  at ${inert(f)}`);
	}
	if (r.systemDialog) {
		lines.push(`System dialog (${r.systemDialog.kind}) of ${quoted(r.systemDialog.package ?? 'an unknown package')}.`);
	}
	for (const w of r.warnings ?? []) lines.push(`Warning: ${inert(w)}`);
	return { action, matched, headline, lines };
}

/** A failed action: the code, message and next step, and the error's details as text. */
export interface ActFailure extends Failure {
	details?: string;
}

const MAX_DETAILS = 2000;

function actFailure(e: unknown): ActFailure {
	const f: ActFailure = failure(e);
	f.message = inert(f.message);
	if (f.nextStep !== undefined) f.nextStep = inert(f.nextStep);
	if (e instanceof CapabilityError && e.details !== undefined) {
		const s = inert(JSON.stringify(e.details));
		f.details = s.length > MAX_DETAILS ? `${s.slice(0, MAX_DETAILS)}...` : s;
	}
	return f;
}

export interface ActView {
	/** The selected device serial; '' for none. */
	device: string;
	fields: TargetFields;
	/** A node picked in the tree, as text: its ref stands alone as the target. */
	picked: string | null;
	typeText: string;
	clear: boolean;
	submit: boolean;
	expect: readonly ExpectRow[];
	timeout: string;
	running: 'tap' | 'type' | null;
	/** Why the form was not sent, in text. */
	invalid: string | null;
	result: ActSummary | null;
	error: ActFailure | null;
}

export class ActModel {
	#client: ActClient;
	#devices: DeviceSelection;
	#listeners = new Set<() => void>();
	#view: ActView | null = null;
	#off: () => void;
	#fields: TargetFields = { id: '', text: '', desc: '', cls: '' };
	#picked: { ref: string; snapshot: string } | null = null;
	#typeText = '';
	#clear = false;
	#submit = false;
	#expect: ExpectRow[] = [];
	#timeout = '';
	#running: 'tap' | 'type' | null = null;
	#invalid: string | null = null;
	#result: ActSummary | null = null;
	#error: ActFailure | null = null;

	constructor(client: ActClient, devices: DeviceSelection) {
		this.#client = client;
		this.#devices = devices;
		this.#off = devices.subscribe(() => this.#changed());
	}

	get view(): ActView {
		this.#view ??= {
			device: this.#devices.view.selected,
			fields: this.#fields,
			picked: this.#picked === null ? null : `${inert(this.#picked.ref)} in snapshot ${inert(this.#picked.snapshot)}`,
			typeText: this.#typeText,
			clear: this.#clear,
			submit: this.#submit,
			expect: this.#expect,
			timeout: this.#timeout,
			running: this.#running,
			invalid: this.#invalid,
			result: this.#result,
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

	/** A tree row's selector fills the target, replacing what was there. */
	pick(s: Selector): void {
		this.#fields = { id: '', text: '', desc: '', cls: '' };
		this.#picked = null;
		if ('ref' in s) this.#picked = { ref: s.ref, snapshot: s.snapshot };
		else if ('id' in s) this.#fields.id = s.id;
		else this.#fields.text = s.text;
		this.#changed();
	}

	/** Typing in a target field replaces a picked ref (the schema forbids both). */
	setField(name: keyof TargetFields, value: string): void {
		this.#fields = { ...this.#fields, [name]: value };
		this.#picked = null;
		this.#changed();
	}

	setTypeText(v: string): void {
		this.#typeText = v;
		this.#changed();
	}
	setClear(v: boolean): void {
		this.#clear = v;
		this.#changed();
	}
	setSubmit(v: boolean): void {
		this.#submit = v;
		this.#changed();
	}
	setTimeoutText(v: string): void {
		this.#timeout = v;
		this.#changed();
	}

	addExpect(): void {
		if (this.#expect.length >= MAX_EXPECT) return;
		this.#expect = [...this.#expect, { kind: 'appears', by: 'text', value: '' }];
		this.#changed();
	}
	removeExpect(i: number): void {
		this.#expect = this.#expect.filter((_, j) => j !== i);
		this.#changed();
	}
	setExpect(i: number, patch: Partial<ExpectRow>): void {
		this.#expect = this.#expect.map((r, j) => (j === i ? { ...r, ...patch } : r));
		this.#changed();
	}

	/** `device.tapElement` on the picked or typed target, with the expectations. */
	async tap(): Promise<void> {
		const common = this.#common();
		if (common === null) return;
		const target = buildTarget(this.#fields, this.#picked);
		if (target === null) return this.#refuse('Pick a node or type a selector to tap.');
		await this.#run('tap', () =>
			this.#client.call('device.tapElement', { device: common.device, target, ...common.rest }),
		);
	}

	/** `device.inputText`; with no target the text goes to the focused field. */
	async type(): Promise<void> {
		const common = this.#common();
		if (common === null) return;
		const target = buildTarget(this.#fields, this.#picked);
		if ([...this.#typeText].length > MAX_TEXT) return this.#refuse(`The text is at most ${MAX_TEXT} characters.`);
		if (this.#clear && target === null) return this.#refuse('Clear needs a target.');
		const params: Params<'device.inputText'> = { device: common.device, text: this.#typeText, ...common.rest };
		if (target !== null) params.target = target;
		if (this.#clear) params.clear = true;
		if (this.#submit) params.submit = true;
		await this.#run('type', () => this.#client.call('device.inputText', params));
	}

	dispose(): void {
		this.#off();
	}

	#common(): { device: string; rest: { expect?: Expect; timeoutMs?: number } } | null {
		if (this.#running !== null) return null;
		const device = this.#devices.view.selected;
		if (device === '') {
			this.#refuse('Select a device first.');
			return null;
		}
		const t = parseTimeout(this.#timeout);
		if (t.error !== undefined) {
			this.#refuse(t.error);
			return null;
		}
		const rest: { expect?: Expect; timeoutMs?: number } = {};
		const expect = buildExpect(this.#expect);
		if (expect !== undefined) rest.expect = expect;
		if (t.ms !== undefined) rest.timeoutMs = t.ms;
		this.#invalid = null;
		return { device, rest };
	}

	#refuse(why: string): void {
		this.#invalid = why;
		this.#changed();
	}

	async #run(action: 'tap' | 'type', call: () => Promise<Parameters<typeof summarize>[1]>): Promise<void> {
		this.#running = action;
		this.#error = null;
		this.#result = null;
		this.#changed();
		try {
			this.#result = summarize(action, await call());
		} catch (e) {
			// A lost connection leaves the action possibly done: say so, never retry it.
			this.#error = actFailure(e);
			if (!(e instanceof CapabilityError)) this.#error.message = `${this.#error.message} The action may have run; read the tree again before repeating it.`;
		} finally {
			this.#running = null;
			this.#changed();
		}
	}

	#changed(): void {
		this.#view = null;
		for (const l of [...this.#listeners]) l();
	}
}
