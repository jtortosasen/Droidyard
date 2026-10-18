import { describe, expect, it } from 'vitest';
import type { Operation } from './activity';
import { buildBlocks, type BlockContext, type BlockFilter } from './blocks';
import { CommandRecordsModel, parseRecord, type CommandsClient } from './commands';
import { keyOf } from '../projects/sessions';

// The wire shape: operation.wait's envelope result is the waited operation's own result
// (restore.test.ts), here the record, not an OperationView.
const wire = { client: 'claude', commands: [{ program: 'gradle', words: ['assembleDebug'], suggested: 'device_install' }] };

function fake(result: unknown, fail = false) {
	const calls: unknown[] = [];
	const client = {
		call(name: string, params: unknown) {
			calls.push([name, params]);
			return fail ? Promise.reject(new Error('gone')) : Promise.resolve(result);
		},
	} as unknown as CommandsClient;
	return { client, calls };
}

const op = (over: Partial<Operation> = {}): Operation => ({
	id: 'r1',
	capability: 'agent.recordCommand',
	workspace: 'w1',
	client: 'cli',
	state: 'completed',
	startedAt: '2026-10-12T10:00:00Z',
	...over,
});
const ctx = (over: Partial<BlockContext> = {}): BlockContext => ({ labels: new Map(), heldBuild: null, ownFailure: null, ...over });
const f = (actor: BlockFilter['actor']): BlockFilter => ({ actor, agentKey: null });

describe('CommandRecordsModel', () => {
	it('reads the result once per row, in the wire shape, and shows actor, title and tool', async () => {
		const { client, calls } = fake(wire);
		const m = new CommandRecordsModel(client);
		await m.load('r1');
		await m.load('r1');
		expect(calls).toEqual([['operation.wait', { operationId: 'r1', timeoutMs: 0 }]]);
		const rec = m.recordOf('r1')!;
		const b = buildBlocks([op()], f('all'), ctx({ records: new Map([['r1', rec]]) }))[0]!;
		expect(b.actor).toBe('Claude Code');
		expect(b.title).toBe('ran `gradle assembleDebug` outside droidyard');
		expect(b.command?.suggested).toBe('device_install');
		expect(b.you).toBe(false);
	});

	it('names Cursor', () => {
		expect(parseRecord({ client: 'cursor', commands: [{ program: 'adb', words: [] }] })?.actor).toBe('Cursor');
	});

	it('does not read an OperationView as the record (a result inside the view is not the wire shape)', async () => {
		const m = new CommandRecordsModel(fake({ id: 'r1', state: 'completed', result: wire }).client);
		await m.load('r1');
		expect(m.recordOf('r1')).toBeUndefined();
	});

	it('leaves the default title when the read fails or is malformed, and does not read again', async () => {
		const bad = fake(undefined, true);
		const m = new CommandRecordsModel(bad.client);
		await m.load('r1');
		await m.load('r1');
		expect(bad.calls).toHaveLength(1);
		expect(m.recordOf('r1')).toBeUndefined();
		const b = buildBlocks([op()], f('all'), ctx())[0]!;
		expect(b.title).toBe('Record command');
		expect(parseRecord('x')).toBeNull();
	});
});

describe('agent.recordCommand rows in Activity', () => {
	it('shows under All and Agents, not under You, whichever client carried the call', () => {
		for (const client of ['cli', 'ui', 'mcp']) {
			const ids = (a: BlockFilter['actor']) => buildBlocks([op({ client })], f(a), ctx()).length;
			expect([ids('all'), ids('agents'), ids('you')]).toEqual([1, 1, 0]);
		}
	});

	it('is no one\'s session row', () => {
		expect(keyOf(op())).toBeNull();
		expect(keyOf(op({ client: 'mcp', session: { id: 's1' } }))).toBeNull();
	});
});

// ADR-0095 §3: the hook's record of an agent's own edit reads as "<agent> edited|created
// <path>", "(outside the project)" when so; the question before the edit is never a row.
describe('agent.recordEdit rows in Activity', () => {
	const edit = (over: Partial<Operation> = {}) => op({ id: 'e1', capability: 'agent.recordEdit', ...over });
	const read = async (wire: unknown) => {
		const m = new CommandRecordsModel(fake(wire).client);
		await m.load('e1', 'agent.recordEdit');
		return m.recordOf('e1');
	};

	it('says edited or created, the path, and the client', async () => {
		const rec = (await read({ client: 'claude', files: [{ path: 'app/Main.kt' }] }))!;
		const b = buildBlocks([edit()], f('all'), ctx({ records: new Map([['e1', rec]]) }))[0]!;
		expect(b.actor).toBe('Claude Code');
		expect(b.title).toBe('edited app/Main.kt');
		expect(b.command).toBeUndefined();
		expect((await read({ client: 'cursor', files: [{ path: 'a.kt', created: true }] }))?.title).toBe('created a.kt');
	});

	it('counts more files and names an edit outside the project', async () => {
		expect((await read({ client: 'claude', files: [{ path: '/tmp/x.txt' }, { path: '/tmp/y.txt' }], outside: true }))?.title).toBe(
			'edited /tmp/x.txt (+1 more) (outside the project)',
		);
	});

	it('shows with the agent that made it, under All and Agents, not under You', () => {
		const joined = edit({ client: 'cli', session: { id: 's1', name: 'claude-code' } });
		const ids = (a: BlockFilter['actor']) => buildBlocks([joined], f(a), ctx()).length;
		expect([ids('all'), ids('agents'), ids('you')]).toEqual([1, 1, 0]);
	});

	it('hides agent.checkWrite whoever carried it', () => {
		for (const client of ['cli', 'ui', 'mcp']) {
			const q = op({ id: 'q1', capability: 'agent.checkWrite', client, session: { id: 's1', name: 'claude-code' } });
			expect(buildBlocks([q], f('all'), ctx())).toEqual([]);
		}
	});

	it('is malformed when the client or the files are not what the record holds', async () => {
		expect(await read({ client: 'vim', files: [{ path: 'a' }] })).toBeUndefined();
		expect(await read({ client: 'claude', files: [] })).toBeUndefined();
	});
});
