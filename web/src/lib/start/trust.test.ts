import { describe, expect, it } from 'vitest';
import { TrustModel, type TrustClient } from './trust';
import { capError, FakeCalls } from '../build/testing';

const policy = (roots: string[], revision: string) => ({
	path: '/cfg/droidyard.json',
	state: 'ok',
	trust: { roots, prefixes: [] },
	devices: { allowPhysical: false, allowlist: [] },
	shell: { enabled: false, roots: [] },
	revision,
});

function setup(roots: string[] = ['/a'], revision = 'r1') {
	const calls = new FakeCalls();
	calls.on('policy.get', () => policy(roots, revision));
	calls.on('policy.update', () => policy([], 'r2'));
	let refreshed = 0;
	const model = new TrustModel(calls as unknown as TrustClient, {
		root: '/p',
		refresh: () => {
			refreshed++;
			return Promise.resolve();
		},
	});
	return { calls, model, refreshed: () => refreshed };
}

describe('TrustModel', () => {
	// Decision (ADR-0015 §5, Merge Patch): arrays are replaced whole, so the grant sends the
	// roots already trusted plus this one, with the revision it read. Sending this root
	// alone would drop every project trusted before.
	it('sends the read roots plus this one with ifRevision, then reads the workspace again', async () => {
		const t = setup(['/a', '/b'], 'rev-9');
		await t.model.grant();
		expect(t.calls.calls.map((c) => c.name)).toEqual(['policy.get', 'policy.update']);
		expect(t.calls.calls[1]!.params).toEqual({ trust: { roots: ['/a', '/b', '/p'] }, ifRevision: 'rev-9' });
		expect(t.refreshed()).toBe(1);
		expect(t.model.view).toMatchObject({ busy: false, conflict: false, error: null });
	});

	// Decision: the click is the only grant; building the model or reading its view never writes.
	it('writes nothing until Trust is clicked', () => {
		const t = setup();
		void t.model.view;
		expect(t.calls.calls).toEqual([]);
	});

	it('does not write a root that is already trusted, and still reads the workspace again', async () => {
		const t = setup(['/p']);
		await t.model.grant();
		expect(t.calls.names()).toEqual(['policy.get']);
		expect(t.refreshed()).toBe(1);
	});

	// Decision (ADR-0045 §5): a conflict re-reads the policy and asks again; it never
	// overwrites a newer file, and it does not retry by itself.
	it('on a conflict reads the policy again, writes nothing more and asks again', async () => {
		const t = setup(['/a'], 'old');
		let n = 0;
		t.calls.on('policy.get', () => (n++ === 0 ? policy(['/a'], 'old') : policy(['/a', '/c'], 'new')));
		t.calls.on('policy.update', () => {
			throw capError('POLICY_REVISION_CONFLICT');
		});
		await t.model.grant();
		expect(t.calls.names()).toEqual(['policy.get', 'policy.update', 'policy.get']);
		expect(t.model.view.conflict).toBe(true);
		expect(t.model.view.error).toBeNull();
		expect(t.refreshed()).toBe(0);

		// The second click carries what the re-read showed.
		t.calls.on('policy.update', (p) => policy([], String(p)));
		await t.model.grant();
		const last = t.calls.calls.at(-1)!;
		expect(last.name).toBe('policy.update');
		expect(last.params).toEqual({ trust: { roots: ['/a', '/c', '/p'] }, ifRevision: 'new' });
		expect(t.model.view.conflict).toBe(false);
		expect(t.refreshed()).toBe(1);
	});

	it('on a conflict that another client resolved by trusting this root, just reads the workspace', async () => {
		const t = setup(['/a'], 'old');
		let n = 0;
		t.calls.on('policy.get', () => (n++ === 0 ? policy(['/a'], 'old') : policy(['/a', '/p'], 'new')));
		t.calls.on('policy.update', () => {
			throw capError('POLICY_REVISION_CONFLICT');
		});
		await t.model.grant();
		expect(t.model.view.conflict).toBe(false);
		expect(t.refreshed()).toBe(1);
	});

	it('shows another refusal by its code', async () => {
		const t = setup();
		t.calls.on('policy.update', () => {
			throw capError('POLICY_CONFIG_UNUSABLE');
		});
		await t.model.grant();
		expect(t.model.view.error).toMatchObject({ code: 'POLICY_CONFIG_UNUSABLE' });
		expect(t.refreshed()).toBe(0);
	});
});
