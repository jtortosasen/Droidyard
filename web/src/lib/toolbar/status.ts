// The toolbar's build status, Xcode-style (plan m2d step 8): "Building :app · 12 s" for
// any client's build of this workspace, read from the build mirror, with the agent's name
// when an agent started it. Pure: the clock is an argument.
//
// The module is the operation's own target (ADR-0076), so any client's `build.assemble`
// names the module it builds, whichever one this tab has selected; `build.run` has no
// target and names none.
import { duration } from '../projects/sessions';
import type { Building } from '../build/mirror';
import { actorOf } from '../ops/blocks';
import { inert } from '../devices/text';

export interface BuildStatus {
	/** "Building :app · 12 s" (a UI build), "Building · 12 s", "Running tasks · 12 s". */
	text: string;
	/** The agent that started it; null for the user's own build. */
	actor: string | null;
	queued: boolean;
	/** The full sentence for a tooltip. */
	title: string;
}

export function buildStatus(
	building: Building | null,
	opts: { now: number; labels: ReadonlyMap<string, string> },
): BuildStatus | null {
	if (building === null) return null;
	const queued = building.state === 'queued';
	const verb = building.capability === 'build.run' ? (queued ? 'Queued tasks' : 'Running tasks') : queued ? 'Queued' : 'Building';
	const module = building.capability === 'build.run' ? '' : (building.target?.module ?? '');
	const what = module === '' ? verb : `${verb} ${inert(module)}`;
	const t = Date.parse(building.startedAt);
	const age = Number.isNaN(t) ? '' : ` · ${duration(Math.max(0, opts.now - t))}`;
	const who = actorOf(building, opts.labels);
	const actor = building.client === 'mcp' ? who : null;
	return {
		text: `${what}${age}`,
		actor,
		queued,
		title: `${building.capability}, ${building.state}, started by ${who}`,
	};
}
