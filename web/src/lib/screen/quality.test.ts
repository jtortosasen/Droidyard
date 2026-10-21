import { describe, expect, it } from 'vitest';
import { CapabilityError } from '../api/client';
import { FakeCalls } from '../build/testing';
import { RemoteQualityModel, type QualityClient } from './quality';

type Entry = { name: string; address: string; addresses?: { address: string; kind: 'vpn' | 'lan' }[]; fingerprint: string; fast?: boolean };

const qa = (extra: Partial<Entry> = {}): Entry => ({
	name: 'qa',
	address: '10.0.0.2:7318',
	addresses: [
		{ address: '10.0.0.2:7318', kind: 'lan' },
		{ address: '100.64.0.2:7318', kind: 'vpn' },
	],
	fingerprint: 'a'.repeat(64),
	...extra,
});
const far = (): Entry => ({ name: 'far', address: '10.9.9.9:7318', fingerprint: 'b'.repeat(64), fast: true });

const conflict = () => new CapabilityError({ code: 'POLICY_REVISION_CONFLICT', message: 'the configuration changed' }, false);

class FakeDevices {
	view: { selected: string; devices: { device: string; host?: string }[] } = {
		selected: 'qa/R58M',
		devices: [{ device: 'emulator-5554' }, { device: 'qa/R58M', host: 'qa' }],
	};
	#l = new Set<() => void>();
	subscribe(l: () => void) {
		this.#l.add(l);
		return () => this.#l.delete(l);
	}
	select(s: string) {
		this.view = { ...this.view, selected: s };
		for (const l of [...this.#l]) l();
	}
}

function setup(remotes: Entry[] = [qa(), far()]) {
	const calls = new FakeCalls();
	const devices = new FakeDevices();
	let revision = 'r1';
	let current = remotes;
	calls.on('policy.get', () => ({ remotes: current, revision }));
	calls.on('policy.update', (p) => {
		current = (p as { remotes: Entry[] }).remotes;
		revision = 'r2';
		return { remotes: current, revision };
	});
	const model = new RemoteQualityModel(calls as unknown as QualityClient, devices);
	return {
		calls,
		devices,
		model,
		set: (r: Entry[], rev: string) => {
			current = r;
			revision = rev;
		},
	};
}

const updates = (t: { calls: FakeCalls }) => t.calls.calls.filter((c) => c.name === 'policy.update').map((c) => c.params);

describe('the quality of a remote device view (plan m2za step 3)', () => {
	it('reads the mark of the selected device\'s remote', async () => {
		const t = setup([qa({ fast: true }), far()]);
		expect(t.model.view).toMatchObject({ remote: 'qa', shown: false });
		await t.model.read();
		expect(t.model.view).toMatchObject({ remote: 'qa', shown: true, fast: true, busy: false, error: null });
	});

	// Decision: a device of this machine has no remote, so no toggle and no read.
	it('has no remote for a local device, and reads nothing', async () => {
		const t = setup();
		t.devices.select('emulator-5554');
		await t.model.read();
		expect(t.model.view).toMatchObject({ remote: '', shown: false, fast: false });
		expect(t.calls.calls).toEqual([]);
	});

	// Decision: a Merge Patch replaces the array whole, so the write sends every entry back as
	// read, with the revision, and changes only the mark. Seen failing with the entry rebuilt
	// from its name and the mark (the addresses and the fingerprint were lost).
	it('writes the whole list back with only the entry\'s mark changed, and the revision', async () => {
		const t = setup();
		await t.model.read();
		await t.model.setFast(true);
		expect(updates(t)).toEqual([{ remotes: [qa({ fast: true }), far()], ifRevision: 'r1' }]);
		expect(t.model.view).toMatchObject({ shown: true, fast: true, busy: false, error: null });
	});

	// Decision: off removes the key (the file's default), not `fast: false`.
	it('removes the key for off', async () => {
		const t = setup([qa({ fast: true }), far()]);
		await t.model.read();
		await t.model.setFast(false);
		const sent = updates(t) as { remotes: Entry[] }[];
		expect(sent).toHaveLength(1);
		expect(sent[0]!.remotes[0]).toEqual(qa());
		expect('fast' in sent[0]!.remotes[0]!).toBe(false);
		expect(sent[0]!.remotes[1]).toEqual(far());
		expect(t.model.view.fast).toBe(false);
	});

	// Decision: an unchanged mark sends nothing; the model shows what the policy says.
	it('sends nothing for a mark that is already so', async () => {
		const t = setup([qa({ fast: true }), far()]);
		await t.model.read();
		await t.model.setFast(true);
		expect(updates(t)).toEqual([]);
		expect(t.model.view).toMatchObject({ fast: true, error: null });
	});

	// Decision: a remote the policy does not list is an error and nothing is written.
	it('writes nothing for a remote that is not listed, and says so', async () => {
		const t = setup([far()]);
		await t.model.setFast(true);
		expect(updates(t)).toEqual([]);
		expect(t.model.view.error?.message).toContain('qa');
		expect(t.model.view.shown).toBe(false);
	});

	// Decision: a concurrent edit is read again and the write decided again, once: the other
	// client's change to another entry survives, and the revision sent is the new one.
	it('retries once after POLICY_REVISION_CONFLICT with the policy read again', async () => {
		const t = setup();
		let n = 0;
		t.calls.on('policy.update', (p) => {
			if (n++ === 0) {
				t.set([qa(), { ...far(), address: '10.9.9.10:7318' }], 'r9');
				throw conflict();
			}
			return { remotes: (p as { remotes: Entry[] }).remotes, revision: 'r10' };
		});
		await t.model.read();
		await t.model.setFast(true);
		expect(updates(t)).toEqual([
			{ remotes: [qa({ fast: true }), far()], ifRevision: 'r1' },
			{ remotes: [qa({ fast: true }), { ...far(), address: '10.9.9.10:7318' }], ifRevision: 'r9' },
		]);
		expect(t.model.view).toMatchObject({ fast: true, error: null });
	});

	it('shows the failure of a second conflict and writes no third time', async () => {
		const t = setup();
		t.calls.on('policy.update', () => {
			throw conflict();
		});
		await t.model.read();
		await t.model.setFast(true);
		expect(updates(t)).toHaveLength(2);
		expect(t.model.view.error).toMatchObject({ code: 'POLICY_REVISION_CONFLICT' });
		expect(t.model.view.busy).toBe(false);
	});

	// Decision: the toggle is disabled while a write is in flight, and a second press is ignored.
	it('is busy during the write and takes no second one', async () => {
		const t = setup();
		await t.model.read();
		let release: () => void = () => {};
		t.calls.on('policy.update', (p) => {
			return new Promise((resolve) => {
				release = () => resolve({ remotes: (p as { remotes: Entry[] }).remotes, revision: 'r2' });
			});
		});
		const first = t.model.setFast(true);
		await Promise.resolve();
		await Promise.resolve();
		expect(t.model.view.busy).toBe(true);
		await t.model.setFast(false);
		expect(updates(t)).toHaveLength(1);
		release();
		await first;
		expect(t.model.view).toMatchObject({ busy: false, fast: true });
	});

	it('follows the selection to another remote\'s mark', async () => {
		const t = setup();
		t.devices.view = { selected: 'far/X1', devices: [{ device: 'far/X1', host: 'far' }] };
		t.devices.select('far/X1');
		await t.model.read();
		expect(t.model.view).toMatchObject({ remote: 'far', shown: true, fast: true });
	});
});
