// What the Git tab calls a root (plan m2n R1; the owner's first check): the workspace `.` is the project's folder
// name, and a submodule is its last path segment, with parent segments added only until no other root has the
// same name (`hev-socks5-server/src/core` against `hev-socks5-tunnel/src/core`). Pure: the full path stays the
// title and the key everywhere else. Every name here is git's or the project's text, never markup.
import { WORKSPACE_ROOT } from './mixed';

/** What the workspace is called when the project's folder has no name to take. */
export const WORKSPACE_FALLBACK = 'workspace';

/** The last segment of the workspace's absolute root (either separator, trailing ones ignored); the fallback when none. */
export function folderName(root: string): string {
	const parts = root.trim().split(/[\\/]+/).filter((p) => p !== '');
	return parts[parts.length - 1] ?? WORKSPACE_FALLBACK;
}

/**
 * The display name of each of `roots`. The workspace's is `workspaceName` (fixed); a submodule's starts as its last
 * segment and takes one more parent segment at a time while another root has the same name, until each is unique or
 * its path is used whole.
 */
export function rootNames(roots: readonly string[], workspaceName: string): Map<string, string> {
	const ws = workspaceName.trim() === '' ? WORKSPACE_FALLBACK : workspaceName.trim();
	const segments = new Map<string, string[]>();
	const depth = new Map<string, number>();
	for (const r of roots) {
		if (r === WORKSPACE_ROOT || segments.has(r)) continue;
		segments.set(r, r.split('/').filter((s) => s !== ''));
		depth.set(r, 1);
	}
	const label = (r: string): string => {
		const s = segments.get(r)!;
		if (s.length === 0) return r;
		return s.slice(Math.max(0, s.length - depth.get(r)!)).join('/');
	};
	for (;;) {
		const groups = new Map<string, string[]>();
		if (roots.includes(WORKSPACE_ROOT)) groups.set(ws, [WORKSPACE_ROOT]);
		for (const r of segments.keys()) {
			const l = label(r);
			const g = groups.get(l);
			if (g) g.push(r);
			else groups.set(l, [r]);
		}
		let grew = false;
		for (const members of groups.values()) {
			if (members.length < 2) continue;
			for (const r of members) {
				if (r === WORKSPACE_ROOT) continue;
				if (depth.get(r)! < segments.get(r)!.length) {
					depth.set(r, depth.get(r)! + 1);
					grew = true;
				}
			}
		}
		if (!grew) break;
	}
	const out = new Map<string, string>();
	for (const r of roots) out.set(r, r === WORKSPACE_ROOT ? ws : label(r));
	return out;
}

/** A root's title: a submodule's full path; the workspace's absolute root when known. */
export const rootTitle = (root: string, workspaceRoot: string): string => (root === WORKSPACE_ROOT ? workspaceRoot.trim() || WORKSPACE_FALLBACK : root);
