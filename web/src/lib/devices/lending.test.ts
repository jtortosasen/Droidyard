import { describe, expect, it } from 'vitest';
import { CapabilityError } from '../api/client';
import { FakeCalls } from '../build/testing';
import type { DeviceHold } from '../projects/sessions';
import { LendingModel, lentRows, type LendingClient } from './lending';

type Lend = { device: string; to: [string, ...string[]] }[];

const policy = (lend: Lend, revision: string) => ({ hosting: { peers: [], lend }, revision });

const conflict = () => new CapabilityError({ code: 'POLICY_REVISION_CONFLICT', message: 'the configuration changed' }, false);

const initial = (): Lend => [
	{ device: 'R58M', to: ['anna'] },
	{ device: 'S2', to: ['bob', 'anna'] },
];

function setup(lend: Lend = initial()) {
	const calls = new FakeCalls();
	let revision = 'r1';
	let current = lend;
	calls.on('policy.get', () => policy(current, revision));
	calls.on('policy.update', (p) => {
		current = (p as { hosting: { lend: Lend } }).hosting.lend;
		revision = 'r2';
		return policy(current, revision);
	});
	const model = new LendingModel(calls as unknown as LendingClient);
	return {
		calls,
		model,
		set: (l: Lend, r: string) => {
			current = l;
			revision = r;
		},
	};
}

describe('what this machine lends (plan m2t step 7)', () => {
	it('reads hosting.lend from the policy', async () => {
		const t = setup();
		await t.model.refresh();
		expect(t.model.view.lent).toEqual(initial());
	});

	// Decision: a Merge Patch replaces an array whole, so Stop lending of one device sends
	// every other entry back, with the revision it read; sending only the removal would
	// stop lending everything.
	it('stops lending one device by sending back the whole remaining lend array and the revision', async () => {
		const t = setup();
		await t.model.refresh();
		await t.model.stopLending('R58M');
		expect(t.calls.calls.filter((c) => c.name === 'policy.update').map((c) => c.params)).toEqual([
			{ hosting: { lend: [{ device: 'S2', to: ['bob', 'anna'] }] }, ifRevision: 'r1' },
		]);
		expect(t.model.view.lent).toEqual([{ device: 'S2', to: ['bob', 'anna'] }]);
		expect(t.model.view.error).toBeNull();
	});

	// Decision: a concurrent edit is read again and the removal decided again, once: the
	// other client's new entry survives, and the revision sent is the new one.
	it('retries once after POLICY_REVISION_CONFLICT with the policy read again', async () => {
		const t = setup();
		let updates = 0;
		t.calls.on('policy.update', (p) => {
			if (updates++ === 0) {
				t.set([{ device: 'R58M', to: ['anna'] }, { device: 'NEW', to: ['bob'] }], 'r9');
				throw conflict();
			}
			return policy((p as { hosting: { lend: Lend } }).hosting.lend, 'r10');
		});
		await t.model.stopLending('R58M');
		expect(t.calls.calls.filter((c) => c.name === 'policy.update').map((c) => c.params)).toEqual([
			{ hosting: { lend: [{ device: 'S2', to: ['bob', 'anna'] }] }, ifRevision: 'r1' },
			{ hosting: { lend: [{ device: 'NEW', to: ['bob'] }] }, ifRevision: 'r9' },
		]);
		expect(t.model.view.error).toBeNull();
	});

	it('shows the failure of a second conflict and writes no third time', async () => {
		const t = setup();
		t.calls.on('policy.update', () => {
			throw conflict();
		});
		await t.model.stopLending('R58M');
		expect(t.calls.calls.filter((c) => c.name === 'policy.update')).toHaveLength(2);
		expect(t.model.view.error?.code).toBe('POLICY_REVISION_CONFLICT');
	});

	it('writes nothing when the device is no longer lent', async () => {
		const t = setup();
		await t.model.stopLending('GONE');
		expect(t.calls.names()).toEqual(['policy.get']);
	});

	// Decision (ADR-0104 §1): a lent device's holder is a peer's session row; the row says
	// who it is lent to and, when held, by whom, so Release has something to release.
	it('joins the lent devices with the holders the session rows give', () => {
		const holds: DeviceHold[] = [
			{ serial: 'R58M', holder: 'anna', idle: false },
			{ serial: 'OTHER', holder: 'claude-code', idle: true },
		];
		expect(lentRows(initial(), holds)).toEqual([
			{ serial: 'R58M', text: 'lent to anna', holder: { serial: 'R58M', holder: 'anna', idle: false } },
			{ serial: 'S2', text: 'lent to bob, anna', holder: undefined },
		]);
	});
});
