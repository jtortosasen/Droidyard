// The New AVD and Edit forms' logic (plan m3f step 9, Q9): the eleven hardware settings
// as the form holds them (text, so an empty field means "the profile's and image's
// default"), their ranges as `emulator.create` documents them, the check made before any
// call, and what Edit sends (only the fields the person changed). The host-dependent upper
// bounds (half the host's memory, its CPU count) are the daemon's to enforce; the form
// holds the fixed ones and shows the range next to the field.
import type { Params } from '../api/capabilities.gen';

export type Settings = NonNullable<Params<'emulator.create'>['settings']>;

export interface NumberField {
	key: 'ramMb' | 'vmHeapMb' | 'internalStorageMb' | 'sdcardMb' | 'cpuCores';
	label: string;
	min: number;
	max: number | null;
	/** The range as shown beside the field. */
	range: string;
	/** SD card: only at creation, 0 meaning none. */
	createOnly?: boolean;
}

export const NUMBER_FIELDS: readonly NumberField[] = [
	{ key: 'ramMb', label: 'RAM (MB)', min: 512, max: 16384, range: '512 to 16384, at most half the host memory' },
	{ key: 'vmHeapMb', label: 'VM heap (MB)', min: 16, max: 1024, range: '16 to 1024, at most half of RAM' },
	{ key: 'internalStorageMb', label: 'Internal storage (MB)', min: 2048, max: 262144, range: '2048 to 262144; an existing AVD never shrinks' },
	{ key: 'sdcardMb', label: 'SD card (MB)', min: 32, max: 65536, range: '0 for none, else 32 to 65536; at creation only', createOnly: true },
	{ key: 'cpuCores', label: 'CPU cores', min: 1, max: null, range: "1 to the host's logical CPUs" },
];

export const DISPLAY_NAME_MAX = 64;

export interface Draft {
	displayName: string;
	ramMb: string;
	vmHeapMb: string;
	internalStorageMb: string;
	sdcardMb: string;
	cpuCores: string;
	graphics: '' | NonNullable<Settings['graphics']>;
	orientation: '' | NonNullable<Settings['orientation']>;
	boot: '' | NonNullable<Settings['boot']>;
	deviceFrame: '' | 'true' | 'false';
	keyboard: '' | 'true' | 'false';
}

export const emptyDraft = (): Draft => ({
	displayName: '',
	ramMb: '',
	vmHeapMb: '',
	internalStorageMb: '',
	sdcardMb: '',
	cpuCores: '',
	graphics: '',
	orientation: '',
	boot: '',
	deviceFrame: '',
	keyboard: '',
});

/** An AVD's current settings as the Edit form prefills them (the SD card is not editable). */
export function draftOf(s: Settings | undefined): Draft {
	if (!s) return emptyDraft();
	const n = (v: number | undefined) => (v === undefined ? '' : String(v));
	return {
		displayName: s.displayName ?? '',
		ramMb: n(s.ramMb),
		vmHeapMb: n(s.vmHeapMb),
		internalStorageMb: n(s.internalStorageMb),
		sdcardMb: '',
		cpuCores: n(s.cpuCores),
		graphics: s.graphics ?? '',
		orientation: s.orientation ?? '',
		boot: s.boot ?? '',
		deviceFrame: s.deviceFrame === undefined ? '' : s.deviceFrame ? 'true' : 'false',
		keyboard: s.keyboard === undefined ? '' : s.keyboard ? 'true' : 'false',
	};
}

export type Problems = Partial<Record<keyof Draft | 'avd' | 'profile' | 'systemImage', string>>;

const NAME = /^[A-Za-z0-9._-]+$/;
const CONTROL = /[\u0000-\u001f\u007f]/;

function whole(text: string): number | null {
	const t = text.trim();
	return /^-?\d+$/.test(t) ? Number(t) : null;
}

/**
 * The problems with a draft, by field; empty when it can be sent. `current` is the AVD's
 * stored settings in Edit (internal storage never below it); absent at creation.
 */
export function draftProblems(d: Draft, current?: Settings): Problems {
	const out: Problems = {};
	if (d.displayName !== '' && (d.displayName.length > DISPLAY_NAME_MAX || CONTROL.test(d.displayName))) {
		out.displayName = `Display name: 1 to ${DISPLAY_NAME_MAX} characters, no control characters.`;
	}
	const editing = current !== undefined;
	for (const f of NUMBER_FIELDS) {
		if (f.createOnly && editing) continue;
		const text = d[f.key].trim();
		if (text === '') continue;
		const v = whole(text);
		const zeroOk = f.key === 'sdcardMb' && v === 0;
		if (v === null || (!zeroOk && (v < f.min || (f.max !== null && v > f.max)))) {
			out[f.key] = `${f.label}: ${f.range}.`;
		}
	}
	const ram = whole(d.ramMb);
	const heap = whole(d.vmHeapMb);
	if (!out.vmHeapMb && !out.ramMb && ram !== null && heap !== null && heap > ram / 2) {
		out.vmHeapMb = `VM heap (MB): at most half of RAM (${Math.floor(ram / 2)}).`;
	}
	const storage = whole(d.internalStorageMb);
	if (!out.internalStorageMb && current?.internalStorageMb !== undefined && storage !== null && storage < current.internalStorageMb) {
		out.internalStorageMb = `Internal storage (MB): not below the current ${current.internalStorageMb}; an AVD never shrinks.`;
	}
	return out;
}

/** Every filled field as a settings object (creation). */
export function settingsOf(d: Draft): Settings {
	const s: Settings = {};
	if (d.displayName !== '') s.displayName = d.displayName;
	for (const f of NUMBER_FIELDS) {
		const v = whole(d[f.key]);
		if (v !== null) s[f.key] = v;
	}
	if (d.graphics !== '') s.graphics = d.graphics;
	if (d.orientation !== '') s.orientation = d.orientation;
	if (d.boot !== '') s.boot = d.boot;
	if (d.deviceFrame !== '') s.deviceFrame = d.deviceFrame === 'true';
	if (d.keyboard !== '') s.keyboard = d.keyboard === 'true';
	return s;
}

/** Edit: only the fields whose value differs from the stored one; the SD card is never sent. */
export function changedSettings(d: Draft, current: Settings | undefined): Settings {
	const all = settingsOf(d);
	delete all.sdcardMb;
	const out: Record<string, unknown> = {};
	for (const k of Object.keys(all) as (keyof Settings)[]) {
		if (all[k] !== current?.[k]) out[k] = all[k];
	}
	return out as Settings;
}

/** What a name must satisfy before `emulator.create` is called. */
export function nameProblem(name: string, listed: readonly string[]): string | undefined {
	if (name === '') return 'Name the AVD.';
	if (!NAME.test(name)) return 'Name: letters, digits, dot, dash and underscore only.';
	const lower = name.toLowerCase();
	if (listed.some((n) => n.toLowerCase() === lower)) return `An AVD named ${name} is already listed.`;
	return undefined;
}
