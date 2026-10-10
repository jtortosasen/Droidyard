import { describe, expect, it } from 'vitest';
import { buildCommands, FileIndex, PaletteModel, segments, type Command, type CommandDeps } from './palette';

type Entries = Parameters<FileIndex['baseline']>[0];

function indexOf(paths: string[], complete = true): FileIndex {
	const idx = new FileIndex();
	idx.baseline(paths.map((path) => ({ path, type: 'file' })) as Entries, complete);
	return idx;
}

// 5,000 paths shaped like an Android project, with decoys that contain the letters of
// "mainact" in the directory or scattered through other names.
function fixture(): string[] {
	const paths: string[] = [];
	for (let i = 0; i < 4990; i++) paths.push(`app/src/main/java/com/example/pkg${i % 97}/Class${i}.kt`);
	paths.push(
		'app/src/main/java/com/example/MainActivity.kt',
		'app/src/main/res/layout/activity_main.xml',
		'app/src/main/java/com/example/main/activity/Helper.kt',
		'app/src/test/java/com/example/MainActivityTest.kt',
		'docs/main-activity-notes.md',
		'app/src/main/java/com/example/MainActionBarController.kt',
		'app/src/main/java/com/example/ui/MaterialAnimationController.kt',
		'app/build.gradle.kts',
		'settings.gradle.kts',
		'README.md',
	);
	return paths;
}

function model(index: FileIndex, commands: () => Command[] = () => [], onopen: (p: string) => void = () => {}): PaletteModel {
	return new PaletteModel(index, commands, onopen);
}

const paths = (m: PaletteModel) => m.view.items.map((i) => (i.kind === 'file' ? i.path : i.command.id));

describe('files mode', () => {
	// Decision: the file name is what the user types, so a name match outranks a match
	// spread over the directory. Reversing the weight (or searching the whole path alone)
	// puts a decoy first.
	it('ranks MainActivity.kt first for "mainact" among 5,000 paths', () => {
		const m = model(indexOf(fixture()));
		m.show('files');
		m.setQuery('mainact');
		expect(paths(m)[0]).toBe('app/src/main/java/com/example/MainActivity.kt');
	});

	it('weights the name over the directory', () => {
		const m = model(indexOf(['foo/bar.kt', 'bar/foo.kt', 'x/y.kt']));
		m.show('files');
		m.setQuery('foo');
		expect(paths(m)).toEqual(['bar/foo.kt', 'foo/bar.kt']);
	});

	// Decision (D26): at most 50 results.
	it('shows at most 50 results', () => {
		const m = model(indexOf(fixture()));
		m.show('files');
		m.setQuery('class');
		expect(m.view.items).toHaveLength(50);
		m.setQuery('');
		expect(m.view.items).toHaveLength(50);
	});

	// Decision: the index is the baseline plus the watch's events, not a second watch.
	it('finds a path an event created and forgets one an event deleted', () => {
		const idx = indexOf(fixture());
		const m = model(idx);
		m.show('files');
		m.setQuery('zzqueue');
		expect(paths(m)).toEqual([]);
		idx.changes([{ kind: 'created', path: 'app/src/main/java/ZzQueue.kt', type: 'file' }] as never);
		m.setQuery('zzqueue');
		expect(paths(m)).toEqual(['app/src/main/java/ZzQueue.kt']);
		idx.changes([{ kind: 'deleted', path: 'app/src/main/java' }] as never);
		m.setQuery('zzqueue');
		expect(paths(m)).toEqual([]);
		expect(idx.entries.some((e) => e.path.startsWith('app/src/main/java/'))).toBe(false);
	});

	it('a created directory is not a file result, and a new baseline replaces the old', () => {
		const idx = indexOf(['a/one.kt']);
		idx.changes([{ kind: 'created', path: 'a/newdir', type: 'dir' }] as never);
		expect(idx.size).toBe(1);
		idx.baseline([{ path: 'b/two.kt', type: 'file' }] as Entries, false);
		expect(idx.entries.map((e) => e.path)).toEqual(['b/two.kt']);
		expect(idx.complete).toBe(false);
	});

	it('marks the matched characters of the name and keeps the directory as text', () => {
		const m = model(indexOf(['a/MainActivity.kt']));
		m.show('files');
		m.setQuery('mact');
		const item = m.view.items[0]!;
		if (item.kind !== 'file') throw new Error('file expected');
		expect(item.name.map((s) => s.text).join('')).toBe('MainActivity.kt');
		expect(item.name.filter((s) => s.hit).map((s) => s.text).join('')).toBe('MAct');
		expect(item.dir).toBe('a');
	});

	it('Enter opens the selected file and closes; arrows wrap', () => {
		const opened: string[] = [];
		const m = model(indexOf(['a.kt', 'b.kt']), () => [], (p) => opened.push(p));
		m.show('files');
		m.move(-1);
		expect(m.view.selected).toBe(1);
		m.choose();
		expect(opened).toEqual(['b.kt']);
		expect(m.view.open).toBe(false);
	});
});

describe('modes', () => {
	// Decision: the mode is the text. ">" is commands; removing it (Backspace on an empty
	// command query) is files again; ⇧⌘P starts with ">".
	it('> switches to commands and Backspace on an empty command query returns to files', () => {
		const cmd: Command = { id: 'x', title: 'Do x', disabled: null, run: () => {} };
		const m = model(indexOf(['a.kt']), () => [cmd]);
		m.show('files');
		expect(m.view.mode).toBe('files');
		m.setQuery('>');
		expect(m.view.mode).toBe('commands');
		expect(paths(m)).toEqual(['x']);
		m.setQuery('');
		expect(m.view.mode).toBe('files');
		expect(paths(m)).toEqual(['a.kt']);
		m.show('commands');
		expect(m.view.query).toBe('>');
		expect(m.view.mode).toBe('commands');
	});
});

describe('segments', () => {
	it('concatenates back to the text', () => {
		const s = segments('hello', [0, 1, 3]);
		expect(s.map((x) => x.text).join('')).toBe('hello');
		expect(s.map((x) => [x.text, x.hit])).toEqual([['he', true], ['l', false], ['l', true], ['o', false]]);
		expect(segments('', [])).toEqual([]);
	});
});

function deps(over: Partial<{ trusted: boolean; building: boolean; avds: { name: string; running?: { device: string } }[] }> = {}) {
	const calls: string[] = [];
	const building = over.building ? ({ operationId: 'o' } as never) : null;
	const d: CommandDeps = {
		workspace: { id: 'w1', trusted: over.trusted ?? true },
		build: {
			view: { open: true, mirror: { building, cancelError: null }, module: ':app', variant: 'debug', starting: false },
			build: () => (calls.push('build'), Promise.resolve()),
		},
		tasks: {
			view: { open: true, trusted: over.trusted ?? true, mirror: { building, cancelError: null }, tasks: [{ path: ':app:lint' }] as never, starting: false },
			clearSelection: () => void calls.push('clear'),
			toggle: (p) => void calls.push(`toggle ${p}`),
			run: () => (calls.push('run'), Promise.resolve()),
		},
		emulators: {
			view: { avds: (over.avds ?? [{ name: 'Pixel' }, { name: 'Tab', running: { device: 'emulator-5554' } }]) as never, starting: false, mirror: { booting: null, stopping: null } },
			start: (a) => (calls.push(`start ${a}`), Promise.resolve()),
			stop: (s) => (calls.push(`stop ${s}`), Promise.resolve()),
		},
		projects: {
			view: { rows: [{ id: 'w1', name: 'Mine' }, { id: 'w2', name: 'Other' }] as never },
			select: (id) => (calls.push(`select ${id}`), Promise.resolve()),
		},
		currentProject: 'w1',
		tree: { view: { showHidden: false }, setShowHidden: (on) => void calls.push(`hidden ${on}`) },
		density: { get: () => 'compact', set: (x) => void calls.push(`density ${x}`) },
		focusTree: () => void calls.push('focus'),
	};
	return { d, calls };
}

describe('commands', () => {
	// Decision: an unavailable command is listed with its reason, and choosing it does nothing.
	it('lists Build disabled with its reason when the project is untrusted, and runs nothing', () => {
		const { d, calls } = deps({ trusted: false });
		const m = model(indexOf([]), () => buildCommands(d));
		m.show('commands');
		m.setQuery('>build');
		const first = m.view.items[0]!;
		if (first.kind !== 'command') throw new Error('command expected');
		expect(first.command.id).toBe('build');
		expect(first.command.disabled).toBe('Trust this project to build it');
		m.choose();
		expect(calls).toEqual([]);
		expect(m.view.open).toBe(true);
	});

	it('runs Build when it can, and says a running build is the reason otherwise', () => {
		const ok = deps();
		const m = model(indexOf([]), () => buildCommands(ok.d));
		m.show('commands');
		m.setQuery('>build');
		m.choose();
		expect(ok.calls).toEqual(['build']);
		expect(m.view.open).toBe(false);
		const busy = deps({ building: true });
		expect(buildCommands(busy.d).find((c) => c.id === 'build')?.disabled).toBe('A build is running');
	});

	it('offers tasks, emulators, other projects and the view toggles', () => {
		const { d, calls } = deps();
		const cmds = buildCommands(d);
		const by = (id: string) => cmds.find((c) => c.id === id)!;
		by('task::app:lint').run();
		by('emulator.start:Pixel').run();
		by('emulator.stop:Tab').run();
		by('project:w2').run();
		by('files.hidden').run();
		by('density').run();
		by('files.focus').run();
		expect(calls).toEqual(['clear', 'toggle :app:lint', 'run', 'start Pixel', 'stop emulator-5554', 'select w2', 'hidden true', 'density comfortable', 'focus']);
		expect(cmds.some((c) => c.id === 'project:w1')).toBe(false);
	});
});
