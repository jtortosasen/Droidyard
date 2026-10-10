import { describe, expect, it } from 'vitest';
import { PANELS, activate, disabledTitle, initialActive, panelsIn } from './panels';

// The ids of the panels m2c shipped, by component; each must live in the shell. Trust is
// inside the workspace panel (its component takes the trust model), so it has no entry.
const PANELS_OF_2C = ['workspace', 'projectfile', 'build', 'logcat', 'applogs', 'console', 'problems', 'activity', 'tasks', 'devices', 'tree', 'act'];

describe('panels', () => {
	// Decision: no 2c panel is lost in the move: each is registered exactly once, in one region.
	it('registers every 2c panel exactly once', () => {
		for (const id of PANELS_OF_2C) expect(PANELS.filter((p) => p.id === id), id).toHaveLength(1);
		const ids = PANELS.map((p) => p.id);
		expect(new Set(ids).size).toBe(ids.length);
	});

	// Decision: where the owner put them (toolbar, bottom tabs, right panel).
	it('places the 2c panels in the owner regions', () => {
		const region = (id: string) => PANELS.find((p) => p.id === id)?.region;
		// Decision (plan m2d step 8): the task selector is a toolbar control; the Tasks tab is gone.
		for (const id of ['workspace', 'build', 'tasks']) expect(region(id)).toBe('toolbar');
		// Decision (plan m2e D35): the project file's indicator lives in the status bar.
		expect(region('projectfile')).toBe('status');
		for (const id of ['logcat', 'applogs', 'console', 'problems', 'activity']) expect(region(id)).toBe('bottom');
		for (const id of ['devices', 'tree', 'act']) expect(region(id)).toBe('right');
	});

	// Decision: a panel that does not exist yet is shown and names the milestone that builds it.
	it('names a milestone on each disabled entry', () => {
		const disabled = PANELS.filter((p) => p.disabled !== undefined);
		expect(disabled.map((p) => p.id).sort()).toEqual(['diff', 'git', 'git-side', 'liveview', 'terminal']);
		for (const p of disabled) {
			expect(p.disabled?.milestone.trim(), p.id).toBeTruthy();
			expect(disabledTitle(p)).toContain(p.disabled!.milestone);
		}
	});

	// Decision: a disabled tab cannot be activated, and the initial tab is never disabled.
	it('cannot activate a disabled or unknown entry', () => {
		const active = initialActive();
		expect(activate(active, 'terminal')).toBe(active);
		expect(activate(active, 'git')).toBe(active);
		expect(activate(active, 'nope')).toBe(active);
		expect(activate(active, 'problems').bottom).toBe('problems');
		for (const r of ['left', 'centre', 'bottom', 'right'] as const) {
			expect(panelsIn(r).find((p) => p.id === active[r])?.disabled, r).toBeUndefined();
		}
	});
});
