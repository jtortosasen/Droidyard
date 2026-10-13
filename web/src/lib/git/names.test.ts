import { describe, expect, it } from 'vitest';
import { WORKSPACE_FALLBACK, folderName, rootNames, rootTitle } from './names';

const src = 'dev/modules/android-vpn/vpn/src/main/cpp';

describe('rootNames', () => {
	// Decision (R1): the last segment is the name, so a submodule is not shown by its long path.
	it('names a root by its last path segment', () => {
		const n = rootNames(['.', 'dev/modules/android-accessibility', 'lib'], 'android-kids-app');
		expect([...n]).toEqual([
			['.', 'android-kids-app'],
			['dev/modules/android-accessibility', 'android-accessibility'],
			['lib', 'lib'],
		]);
	});

	// Decision (R1): parent segments are added only until the names are unique; roots already unique keep the short name.
	it('adds parent segments to roots that share a name, and only to them', () => {
		const a = `${src}/hev-socks5-server/src/core`;
		const b = `${src}/hev-socks5-tunnel/src/core`;
		const n = rootNames(['.', a, b, 'lib/other'], 'app');
		expect(n.get(a)).toBe('hev-socks5-server/src/core');
		expect(n.get(b)).toBe('hev-socks5-tunnel/src/core');
		expect(n.get('lib/other')).toBe('other');
	});

	// Decision (R1): a name that is the end of another root's path (nesting) still ends up unique, one more segment at a time.
	it('separates a root from one whose path ends the same way', () => {
		const n = rootNames(['.', 'x/core', 'y/x/core', 'core'], 'app');
		expect(n.get('core')).toBe('core');
		expect(n.get('x/core')).toBe('x/core');
		expect(n.get('y/x/core')).toBe('y/x/core');
		expect(new Set(n.values()).size).toBe(4);
	});

	// Decision (R1): the workspace's name is fixed (the project's folder); a submodule called the same takes its parent instead.
	it('shows the workspace as the project folder and moves a submodule with the same name', () => {
		const n = rootNames(['.', 'mods/app', 'app'], 'app');
		expect(n.get('.')).toBe('app');
		expect(n.get('mods/app')).toBe('mods/app');
		expect(n.get('app')).toBe('app');
	});

	// Decision (R1): without a folder name the workspace is "workspace"; the roots given come back whatever they are.
	it('falls back to "workspace" and keeps the roots asked for', () => {
		expect(rootNames(['.'], '  ').get('.')).toBe(WORKSPACE_FALLBACK);
		expect([...rootNames(['lib'], 'p').keys()]).toEqual(['lib']);
	});
});

describe('folderName and rootTitle', () => {
	it('takes the last segment of the root, ignoring trailing separators and Windows ones', () => {
		expect(folderName('/Users/me/android-kids-app/')).toBe('android-kids-app');
		expect(folderName('C:\\work\\app')).toBe('app');
		expect(folderName('')).toBe(WORKSPACE_FALLBACK);
		expect(folderName('/')).toBe(WORKSPACE_FALLBACK);
	});
	it('titles a submodule with its path and the workspace with its root', () => {
		expect(rootTitle('a/b', '/p')).toBe('a/b');
		expect(rootTitle('.', ' /p ')).toBe('/p');
		expect(rootTitle('.', '')).toBe(WORKSPACE_FALLBACK);
	});
});
