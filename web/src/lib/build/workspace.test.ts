import { describe, expect, it } from 'vitest';
import { WorkspaceModel, type WorkspaceClient } from './workspace';
import { ConnectionError } from '../api/client';
import { capError, FakeCalls, hubStarted, settle } from './testing';

async function setup() {
	const { events, hub } = await hubStarted();
	const calls = new FakeCalls();
	calls.on('workspace.open', () => ({ id: 'w1', name: 'sample', root: '/p', state: 'ready', trusted: true }));
	calls.on('workspace.describe', () => ({ id: 'w1', name: 'sample', root: '/p', state: 'ready', trusted: true }));
	const model = new WorkspaceModel(calls as unknown as WorkspaceClient, hub);
	return { events, hub, calls, model };
}

const life = (events: Awaited<ReturnType<typeof setup>>['events'], workspace: string, state: string) =>
	events.emit(0, { topic: 'workspace.lifecycle', type: state, workspace, payload: { state } });

describe('WorkspaceModel', () => {
	// Decision (m2c step 10, ADR-0045 §2): the tab's project is opened by the id from the
	// address, and the root the daemon answers with is what the other panels address.
	// Nothing is read from or kept in storage.
	it('opens by the id from the address and learns the root from the answer', async () => {
		const t = await setup();
		expect(t.model.root).toBe('');
		expect(await t.model.openById('w1')).toBe(true);
		expect(t.calls.calls).toEqual([{ name: 'workspace.open', params: {}, workspace: 'w1' }]);
		expect(t.model.id).toBe('w1');
		expect(t.model.root).toBe('/p');
		expect(t.model.view.info?.trusted).toBe(true);
	});

	// Decision (D10): a refusal shows its code (WORKSPACE_UNTRUSTED names `droidyard trust`).
	it('shows the code and next step of a refused open', async () => {
		const t = await setup();
		t.calls.on('workspace.open', () => {
			throw Object.assign(capError('WORKSPACE_UNTRUSTED'), { nextStep: 'run droidyard trust' });
		});
		expect(await t.model.openById('w1')).toBe(false);
		expect(t.model.view.error).toMatchObject({ code: 'WORKSPACE_UNTRUSTED', nextStep: 'run droidyard trust' });
		expect(t.model.id).toBeNull();
	});

	// Decision (§Changes): a workspace closed by another client is said so and offered
	// again; it is not reopened by itself.
	it('says the open workspace was closed by another client and reopens only on request', async () => {
		const t = await setup();
		await t.model.openById('w1');
		life(t.events, 'w1', 'closed');
		expect(t.model.view.closedByOther).toBe(true);
		expect(t.model.id).toBeNull();
		expect(t.calls.names()).toEqual(['workspace.open']);
		await t.model.open();
		// The reopen addresses the root now known, not the id.
		expect(t.calls.calls[1]).toEqual({ name: 'workspace.open', params: {}, workspace: '/p' });
		expect(t.model.view.closedByOther).toBe(false);
		expect(t.model.id).toBe('w1');
	});

	// Decision (§Changes): a workspace another client opens or closes shows.
	it('shows workspaces seen on workspace.lifecycle', async () => {
		const t = await setup();
		life(t.events, 'w9', 'ready');
		expect(t.model.view.seen).toEqual([{ id: 'w9', state: 'ready' }]);
		life(t.events, 'w9', 'closed');
		expect(t.model.view.seen).toEqual([{ id: 'w9', state: 'closed' }]);
	});

	// Decision: after events may have been missed, the daemon is asked; its refusal means closed.
	it('asks the daemon on a resync and marks a workspace it no longer knows as closed', async () => {
		const t = await setup();
		await t.model.openById('w1');
		t.calls.on('workspace.describe', () => {
			throw capError('WORKSPACE_NOT_OPEN');
		});
		t.events.emit(0, { topic: 'events.gap', type: 'gap', payload: {} });
		await settle();
		expect(t.model.view.closedByOther).toBe(true);
	});

	// Decision: only the daemon's answer says a workspace is gone; a lost connection says nothing.
	it('does not mark the workspace closed when the resync call is lost', async () => {
		const t = await setup();
		await t.model.openById('w1');
		t.calls.on('workspace.describe', () => {
			throw new ConnectionError('lost');
		});
		t.events.emit(0, { topic: 'events.gap', type: 'gap', payload: {} });
		await settle();
		expect(t.model.view.closedByOther).toBe(false);
	});

	// Decision (ADR-0045 §5): after a grant the model reads the workspace again, because no
	// event carries trust.
	it('reads the trusted mark again on request', async () => {
		const t = await setup();
		t.calls.on('workspace.open', () => ({ id: 'w1', name: 'sample', root: '/p', state: 'ready', trusted: false }));
		await t.model.openById('w1');
		expect(t.model.view.info?.trusted).toBe(false);
		t.calls.on('workspace.describe', () => ({ id: 'w1', name: 'sample', root: '/p', state: 'ready', trusted: true }));
		await t.model.refresh();
		expect(t.model.view.info?.trusted).toBe(true);
		expect(t.calls.calls.at(-1)).toEqual({ name: 'workspace.describe', params: {}, workspace: '/p' });
	});

	// Decision (ADR-0075 §5, §7): `gradle` comes from open, and describe is the one source
	// afterwards: a root chosen elsewhere shows after refresh().
	it('keeps gradle from open and takes it again from describe on refresh', async () => {
		const t = await setup();
		t.calls.on('workspace.open', () => ({ id: 'w1', name: 's', root: '/p', state: 'ready', trusted: true, gradle: { candidates: ['dev', 'tools/x'] } }));
		await t.model.openById('w1');
		expect(t.model.view.gradle).toEqual({ candidates: ['dev', 'tools/x'] });
		expect(t.model.gradleRoot).toBeUndefined();
		t.calls.on('workspace.describe', () => ({ id: 'w1', name: 's', root: '/p', state: 'ready', trusted: true, gradle: { root: 'dev', candidates: ['dev', 'tools/x'] } }));
		await t.model.refresh();
		expect(t.model.view.gradle).toEqual({ root: 'dev', candidates: ['dev', 'tools/x'] });
		expect(t.model.gradleRoot).toBe('dev');
	});
});
