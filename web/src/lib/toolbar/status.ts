// The toolbar's build status, Xcode-style (plan m2d step 8): "Building :app · 12 s" for
// any client's build of this workspace, read from the build mirror, with the agent's name
// when an agent started it. Pure: the clock is an argument.
//
// The operation view carries no module (ADR-0043 §2), so ":app" is named only for a UI build,
// whose module is the one this tab has selected; another client's build says "Building"
// alone rather than guess, and `build.run` names none.
import { duration } from '../projects/sessions';
import type { Building } from '../build/mirror';
import { actorOf } from '../ops/blocks';

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
	opts: { module: string; now: number; labels: ReadonlyMap<string, string> },
): BuildStatus | null {
	if (building === null) return null;
	const queued = building.state === 'queued';
	const verb = building.capability === 'build.run' ? (queued ? 'Queued tasks' : 'Running tasks') : queued ? 'Queued' : 'Building';
	const known = building.client === 'ui' && opts.module !== '';
	const what = building.capability === 'build.run' || !known ? verb : `${verb} ${opts.module}`;
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
