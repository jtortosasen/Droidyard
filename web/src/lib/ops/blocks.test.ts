import { describe, expect, it } from 'vitest';
import type { Operation } from './activity';
import { buildBlocks, elapsedText, titleOf, type BlockContext, type BlockFilter } from './blocks';

const op = (id: string, over: Partial<Operation> = {}): Operation => ({
	id,
	capability: 'build.assemble',
	workspace: 'w1',
	client: 'mcp',
	session: { id: 's1', name: 'claude' },
	state: 'completed',
	startedAt: '2026-10-10T10:00:00Z',
	...over,
});
const ctx = (over: Partial<BlockContext> = {}): BlockContext => ({ labels: new Map(), heldBuild: null, ownFailure: null, ...over });
const all: BlockFilter = { actor: 'all', agentKey: null };
const one = (o: Operation, c = ctx()) => buildBlocks([o], all, c)[0]!;

describe('activity blocks: output', () => {
	// Decision (plan m2d step 8): a build the console holds shows its lines, by buildId (= operation id).
	it('shows the console of the build the console model holds', () => {
		expect(one(op('b1'), ctx({ heldBuild: 'b1' })).output).toEqual({ kind: 'console', buildId: 'b1' });
	});

	// Decision: a build the console does not hold is read in the Build output tab.
	it('offers Build output for a build the console no longer holds', () => {
		expect(one(op('b1'), ctx({ heldBuild: 'b2' })).output).toEqual({ kind: 'build-output' });
	});

	// Decision: the tab's own failed call shows its code, and the message the tab has.
	it("shows the tab's own failed call with its code and message", () => {
		const failed = op('u1', { client: 'ui', session: undefined, state: 'failed', code: 'WORKSPACE_UNTRUSTED' });
		const b = buildBlocks([failed], all, ctx({ ownFailure: { code: 'WORKSPACE_UNTRUSTED', message: 'trust it first' } }))[0]!;
		expect(b.output).toEqual({ kind: 'error', code: 'WORKSPACE_UNTRUSTED', message: 'trust it first' });
		// the message is the newest such failure's only
		const older = op('u0', { client: 'ui', session: undefined, state: 'failed', code: 'WORKSPACE_UNTRUSTED' });
		const [first, second] = buildBlocks([failed, older], all, ctx({ ownFailure: { code: 'WORKSPACE_UNTRUSTED', message: 'm' } }));
		expect(first!.id).toBe('u0');
		expect(first!.output).toEqual({ kind: 'error', code: 'WORKSPACE_UNTRUSTED', message: undefined });
		expect(second!.output).toEqual({ kind: 'error', code: 'WORKSPACE_UNTRUSTED', message: 'm' });
	});

	// Decision (ADR-0043 §2): another client's non-build operation has no output; nothing is invented.
	it("shows no output for another client's non-build operation", () => {
		expect(one(op('x1', { capability: 'device.list', state: 'failed', code: 'DEVICE_GONE' })).output).toEqual({ kind: 'none' });
	});

	// Decision: a queued build has not run; it shows what it waits behind, not an output.
	it('shows blockedBy on a queued block and no output', () => {
		const b = one(op('q1', { state: 'queued', blockedBy: { capability: 'build.assemble', reason: 'one build at a time' } }));
		expect(b.tone).toBe('queued');
		expect(b.blockedBy?.reason).toBe('one build at a time');
		expect(b.output.kind).toBe('none');
	});
});

describe('activity blocks: stream, actor and filters', () => {
	// Decision: the stream grows at the bottom, so the store's newest-first list is reversed.
	it('lists oldest first', () => {
		expect(buildBlocks([op('n'), op('o')], all, ctx()).map((b) => b.id)).toEqual(['o', 'n']);
	});

	// Decision (D30): the actor is the projects panel's label, "You" for the user's commands.
	it('names the actor', () => {
		expect(one(op('a'), ctx({ labels: new Map([['s1', 'claude 2']]) })).actor).toBe('claude 2');
		expect(one(op('a')).actor).toBe('claude');
		const you = one(op('y', { client: 'ui', session: undefined, capability: 'build.assemble' }));
		expect(you.actor).toBe('You');
		expect(you.you).toBe(true);
	});

	// Decision (m2b D16): the user's own reads are not activity; an agent's reads are.
	it('hides the user reads, keeps the agent reads', () => {
		const ids = buildBlocks(
			[op('r1', { client: 'ui', session: undefined, capability: 'build.tasks' }), op('r2', { capability: 'build.tasks' })],
			all,
			ctx(),
		).map((b) => b.id);
		expect(ids).toEqual(['r2']);
	});

	// Decision: All / You / Agents, and the selected agent row (D30) narrows to its session.
	it('filters by who and by the selected agent', () => {
		const ops = [
			op('a1'),
			op('a2', { session: { id: 's2', name: 'codex' } }),
			op('u1', { client: 'ui', session: undefined }),
		];
		const ids = (f: BlockFilter) => buildBlocks(ops, f, ctx()).map((b) => b.id).sort();
		expect(ids({ actor: 'you', agentKey: null })).toEqual(['u1']);
		expect(ids({ actor: 'agents', agentKey: null })).toEqual(['a1', 'a2']);
		expect(ids({ actor: 'all', agentKey: 's2' })).toEqual(['a2']);
		expect(ids({ actor: 'all', agentKey: 'you' })).toEqual(['u1']);
	});

	it('puts the capability in words', () => {
		expect(titleOf('build.assemble')).toBe('Build');
		expect(titleOf('device.listAll')).toBe('List all');
		expect(titleOf('ui.tap')).toBe('Tap');
	});
});

describe('activity blocks: duration', () => {
	// Decision: a live block ticks from its start; a finished one shows the daemon's duration, fixed.
	it('ticks while live and is fixed once ended', () => {
		const now = Date.parse('2026-10-10T10:00:12Z');
		const live = one(op('l', { state: 'running' }));
		expect(elapsedText(live, now)).toBe('12 s');
		expect(elapsedText(live, now + 5000)).toBe('17 s');
		const done = one(op('d', { durationMs: 850 }));
		expect(elapsedText(done, now)).toBe('850 ms');
		expect(elapsedText(done, now + 60_000)).toBe('850 ms');
		expect(elapsedText(one(op('u', { state: 'unseen' })), now)).toBe('');
	});
});

describe('activity blocks: target', () => {
	// Decision (A2, ADR-0076): a block shows what the operation names, beside the actor:
	// the device, else the AVD, else the module; none when it names none.
	it('shows the device an input names, and nothing when there is no target', () => {
		const tap = op('t1', { capability: 'device.tap', client: 'ui', session: undefined, target: { device: 'emulator-5554' } });
		expect(one(tap).target).toBe('emulator-5554');
		expect(one(op('t2', { capability: 'device.tap', client: 'ui', session: undefined })).target).toBe('');
		expect(one(op('t3', { target: { avd: 'Pixel_8', module: ':app' } })).target).toBe('Pixel_8');
		expect(one(op('t4', { target: { module: ':app' } })).target).toBe(':app');
	});
});
