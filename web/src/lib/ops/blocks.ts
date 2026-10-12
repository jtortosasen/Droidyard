// The activity feed as blocks (plan m2d step 8, D30): one block per operation, derived from
// the activity store's rows. Pure: the clock, the session labels and what the console holds
// come in as arguments, so the rules are tested without a component.
//
// Targets are not shown (ADR-0043 §2: the operation view carries none), so a block's title
// is the capability in words, and its full name is the tooltip. Output is a rule of four:
//   - a build the console model holds: its lines, by `buildId` (a build's id is its
//     operation's id, ADR-0020);
//   - the tab's own call that failed: its code, and the message when the tab has it;
//   - another build, or one the console no longer holds: "open in Build output";
//   - anything else (another client's non-build operation): nothing, as nothing is known.
import { BUILD_CAPABILITIES } from '../build/mirror';
import { duration, keyOf } from '../projects/sessions';
import { isTerminal, type Operation } from './activity';

export type BlockTone = 'running' | 'queued' | 'success' | 'failure' | 'cancelled' | 'unseen';

export type BlockOutput =
	| { kind: 'console'; buildId: string }
	| { kind: 'error'; code: string; message?: string }
	| { kind: 'build-output' }
	| { kind: 'none' };

export interface Block {
	id: string;
	capability: string;
	/** The workspace id; null for a global operation. */
	workspace: string | null;
	/** The capability in words ("Build", "Run tasks", "Tap"). */
	title: string;
	actor: string;
	you: boolean;
	tone: BlockTone;
	/** "Ended unseen" for a state the store could not see. */
	stateText: string;
	startedAt: string;
	/** Fixed once the operation ended and the daemon said how long it took. */
	durationMs?: number;
	/** The operation is queued or running: its duration ticks. */
	ticking: boolean;
	blockedBy?: Operation['blockedBy'];
	output: BlockOutput;
}

export interface BlockContext {
	/** session id -> the label the projects panel shows (D30: duplicate names numbered). */
	labels: ReadonlyMap<string, string>;
	/** The build the console model holds, if any. */
	heldBuild: string | null;
	/** The tab's own last refused call, with its message (the activity feed carries a code only). */
	ownFailure: { code?: string; message: string } | null;
}

export type ActorFilter = 'all' | 'you' | 'agents';

export interface BlockFilter {
	actor: ActorFilter;
	/** The projects panel's selected row (a session id, or "you"); null shows all. */
	agentKey: string | null;
}

const OVERRIDES: Record<string, string> = {
	'build.assemble': 'Build',
	'build.run': 'Run tasks',
};

/** "build.assemble" -> "Build"; otherwise the last segment, split at capitals: "device.listAll" -> "List all". */
export function titleOf(capability: string): string {
	const known = OVERRIDES[capability];
	if (known) return known;
	const last = capability.slice(capability.lastIndexOf('.') + 1);
	const words = last.replace(/([a-z0-9])([A-Z])/g, '$1 $2').toLowerCase();
	return words.charAt(0).toUpperCase() + words.slice(1);
}

export function actorOf(op: Pick<Operation, 'client' | 'session'>, labels: ReadonlyMap<string, string>): string {
	if (op.client === 'ui' || op.client === 'cli') return 'You';
	if (op.client === 'mcp') return (op.session && labels.get(op.session.id)) || op.session?.name || 'agent';
	return 'Unknown';
}

/** The user's reads (the tab's own, and the CLI's) are not activity: m2b D16. */
function hidden(op: Operation): boolean {
	return keyOf(op) === null && (op.client === 'ui' || op.client === 'cli');
}

function toneOf(op: Operation): BlockTone {
	switch (op.state) {
		case 'queued':
			return 'queued';
		case 'running':
			return 'running';
		case 'completed':
			return 'success';
		case 'failed':
			return 'failure';
		case 'cancelled':
			return 'cancelled';
		default:
			return 'unseen';
	}
}

export function outputOf(op: Operation, ctx: Pick<BlockContext, 'heldBuild'>, message?: string): BlockOutput {
	const isBuild = (BUILD_CAPABILITIES as readonly string[]).includes(op.capability);
	if (isBuild && ctx.heldBuild === op.id) return { kind: 'console', buildId: op.id };
	if (op.client === 'ui' && op.state === 'failed' && op.code) return { kind: 'error', code: op.code, message };
	if (isBuild && op.state !== 'queued') return { kind: 'build-output' };
	return { kind: 'none' };
}

/** The block of one operation; `message` is the tab's own failure text for it, when it has one. */
export function blockOf(op: Operation, ctx: BlockContext, message?: string): Block {
	return {
		id: op.id,
		capability: op.capability,
		workspace: op.workspace,
		title: titleOf(op.capability),
		actor: actorOf(op, ctx.labels),
		you: op.client === 'ui' || op.client === 'cli',
		tone: toneOf(op),
		stateText: op.state === 'unseen' ? 'ended unseen' : op.state,
		startedAt: op.startedAt,
		durationMs: op.durationMs,
		ticking: !isTerminal(op.state),
		blockedBy: op.blockedBy,
		output: outputOf(op, ctx, message),
	};
}

function admitted(op: Operation, f: BlockFilter): boolean {
	if (hidden(op)) return false;
	if (f.actor === 'you' && op.client !== 'ui' && op.client !== 'cli') return false;
	if (f.actor === 'agents' && op.client !== 'mcp') return false;
	return f.agentKey === null || keyOf(op) === f.agentKey;
}

/**
 * The blocks to draw, oldest first (the stream grows at the bottom). `operations` is the
 * store's list, newest first. The tab's own failure message goes to the newest failed call
 * of its code only: an older block of the same code is not that failure.
 */
export function buildBlocks(operations: readonly Operation[], filter: BlockFilter, ctx: BlockContext): Block[] {
	let ownDone = ctx.ownFailure === null;
	const out: Block[] = [];
	for (const op of operations) {
		if (!admitted(op, filter)) continue;
		let message: string | undefined;
		if (!ownDone && op.client === 'ui' && op.state === 'failed' && op.code === ctx.ownFailure?.code) {
			message = ctx.ownFailure?.message;
			ownDone = true;
		}
		out.push(blockOf(op, ctx, message));
	}
	return out.reverse();
}

/** "850 ms", "12 s", "3 m 05 s": a ticking block's age from the clock, else the daemon's duration; '' when unknown. */
export function elapsedText(b: Pick<Block, 'ticking' | 'startedAt' | 'durationMs'>, now: number): string {
	let ms: number | undefined;
	if (b.ticking) {
		const t = Date.parse(b.startedAt);
		ms = Number.isNaN(t) ? undefined : Math.max(0, now - t);
	} else {
		ms = b.durationMs;
	}
	if (ms === undefined) return '';
	return ms < 1000 && !b.ticking ? `${ms} ms` : duration(ms);
}
