import { describe, expect, it } from 'vitest';
import { ROOT_KEY, WorkspaceModel, type RootStorage, type WorkspaceClient } from './workspace';
import { ConnectionError } from '../api/client';
import { capError, FakeCalls, hubStarted, settle } from './testing';

function storage(initial?: string): RootStorage & { data: Map<string, string> } {
	const data = new Map<string, string>(initial === undefined ? [] : [[ROOT_KEY, initial]]);
	return { data, getItem: (k) => data.get(k) ?? null, setItem: (k, v) => void data.set(k, v) };
}

async function setup(initial?: string) {
	const { events, hub } = await hubStarted();
	const calls = new FakeCalls();
	calls.on('workspace.open', () => ({ id: 'w1', name: 'sample', root: '/p', state: 'ready', trusted: true }));
	calls.on('workspace.describe', () => ({ id: 'w1', name: 'sample', root: '/p', state: 'ready', trusted: true }));
	const st = storage(initial);
	const model = new WorkspaceModel(calls as unknown as WorkspaceClient, hub, st);
	return { events, hub, calls, st, model };
}

const life = (events: Awaited<ReturnType<typeof setup>>['events'], workspace: string, state: string) =>
	events.emit(0, { topic: 'workspace.lifecycle', type: state, workspace, payload: { state } });

describe('WorkspaceModel', () => {
	// Decision (D10): the typed root is kept and offered again; the open call carries it as
	// the envelope's workspace, and its id is what the panels match events on.
	it('keeps the typed root, opens with it and exposes the id', async () => {
		const t = await setup('/kept');
		expect(t.model.root).toBe('/kept');
		t.model.setRoot('/p');
		expect(t.st.data.get(ROOT_KEY)).toBe('/p');
		await t.model.open();
		expect(t.calls.calls).toEqual([{ name: 'workspace.open', params: {}, workspace: '/p' }]);
		expect(t.model.id).toBe('w1');
		expect(t.model.view.info?.trusted).toBe(true);
	});

	// Decision (D10): a refusal shows its code (WORKSPACE_UNTRUSTED names `droidyard trust`).
	it('shows the code and next step of a refused open', async () => {
		const t = await setup('/p');
		t.calls.on('workspace.open', () => {
			throw Object.assign(capError('WORKSPACE_UNTRUSTED'), { nextStep: 'run droidyard trust' });
		});
		await t.model.open();
		expect(t.model.view.error).toMatchObject({ code: 'WORKSPACE_UNTRUSTED', nextStep: 'run droidyard trust' });
		expect(t.model.id).toBeNull();
	});

	// Decision (§Changes): a workspace closed by another client is said so and offered
	// again; it is not reopened by itself.
	it('says the open workspace was closed by another client and reopens only on request', async () => {
		const t = await setup('/p');
		await t.model.open();
		life(t.events, 'w1', 'closed');
		expect(t.model.view.closedByOther).toBe(true);
		expect(t.model.id).toBeNull();
		expect(t.calls.names()).toEqual(['workspace.open']);
		await t.model.open();
		expect(t.model.view.closedByOther).toBe(false);
		expect(t.model.id).toBe('w1');
	});

	// Decision (§Changes): a workspace another client opens or closes shows.
	it('shows workspaces seen on workspace.lifecycle', async () => {
		const t = await setup('/p');
		life(t.events, 'w9', 'ready');
		expect(t.model.view.seen).toEqual([{ id: 'w9', state: 'ready' }]);
		life(t.events, 'w9', 'closed');
		expect(t.model.view.seen).toEqual([{ id: 'w9', state: 'closed' }]);
	});

	// Decision: after events may have been missed, the daemon is asked; its refusal means closed.
	it('asks the daemon on a resync and marks a workspace it no longer knows as closed', async () => {
		const t = await setup('/p');
		await t.model.open();
		t.calls.on('workspace.describe', () => {
			throw capError('WORKSPACE_NOT_OPEN');
		});
		t.events.emit(0, { topic: 'events.gap', type: 'gap', payload: {} });
		await settle();
		expect(t.model.view.closedByOther).toBe(true);
	});

	// Decision: only the daemon's answer says a workspace is gone; a lost connection says nothing.
	it('does not mark the workspace closed when the resync call is lost', async () => {
		const t = await setup('/p');
		await t.model.open();
		t.calls.on('workspace.describe', () => {
			throw new ConnectionError('lost');
		});
		t.events.emit(0, { topic: 'events.gap', type: 'gap', payload: {} });
		await settle();
		expect(t.model.view.closedByOther).toBe(false);
	});
});
