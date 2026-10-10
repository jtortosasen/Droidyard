// The registry the shell mounts from (plan m2d step 2): every panel has an id, a region,
// a caption, and, when it is not built yet, the milestone that will build it. A disabled
// entry is shown and cannot be activated. The 2c panels keep their behaviour; only their
// place changes.

export type PanelRegion = 'toolbar' | 'left' | 'centre' | 'bottom' | 'right';

export interface PanelDef {
	id: string;
	region: PanelRegion;
	label: string;
	/** Set on a panel that does not exist yet: the milestone that builds it. */
	disabled?: { milestone: string };
}

export const PANELS: readonly PanelDef[] = [
	{ id: 'workspace', region: 'toolbar', label: 'Workspace' },
	{ id: 'build', region: 'toolbar', label: 'Build' },
	{ id: 'projectfile', region: 'toolbar', label: 'Project file' },

	{ id: 'files', region: 'left', label: 'Editor' },
	{ id: 'git-side', region: 'left', label: 'Git', disabled: { milestone: 'Git' } },

	{ id: 'terminal', region: 'centre', label: 'Terminal', disabled: { milestone: 'agent sessions' } },
	{ id: 'editor', region: 'centre', label: 'Editor' },
	{ id: 'diff', region: 'centre', label: 'Diff', disabled: { milestone: 'Git' } },

	{ id: 'logcat', region: 'bottom', label: 'Logcat' },
	{ id: 'applogs', region: 'bottom', label: 'App logs' },
	{ id: 'console', region: 'bottom', label: 'Build output' },
	{ id: 'problems', region: 'bottom', label: 'Problems' },
	{ id: 'activity', region: 'bottom', label: 'Activity' },
	{ id: 'tasks', region: 'bottom', label: 'Tasks' },
	{ id: 'git', region: 'bottom', label: 'Git', disabled: { milestone: 'Git' } },

	{ id: 'devices', region: 'right', label: 'Devices' },
	{ id: 'liveview', region: 'right', label: 'Live view', disabled: { milestone: 'live emulator view' } },
	{ id: 'tree', region: 'right', label: 'UI tree' },
	{ id: 'act', region: 'right', label: 'Actions' },
];

export function panelsIn(region: PanelRegion): PanelDef[] {
	return PANELS.filter((p) => p.region === region);
}

/** The id each tabbed region starts on: its first panel that is not disabled. */
export function initialActive(): Record<PanelRegion, string> {
	const active = {} as Record<PanelRegion, string>;
	for (const region of ['toolbar', 'left', 'centre', 'bottom', 'right'] as const) {
		active[region] = panelsIn(region).find((p) => p.disabled === undefined)?.id ?? '';
	}
	return active;
}

/** The tooltip of a disabled tab. */
export function disabledTitle(panel: PanelDef): string {
	return panel.disabled ? `${panel.label}: arrives with the ${panel.disabled.milestone} milestone` : panel.label;
}

/** `id` made the active panel of its region; a disabled or unknown id changes nothing. */
export function activate(active: Record<PanelRegion, string>, id: string): Record<PanelRegion, string> {
	const panel = PANELS.find((p) => p.id === id);
	if (panel === undefined || panel.disabled !== undefined) return active;
	return { ...active, [panel.region]: id };
}
