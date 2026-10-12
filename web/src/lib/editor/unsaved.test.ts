import { describe, expect, it, vi } from 'vitest';
import type { Params, Result } from '../api/capabilities.gen';
import { capError } from '../build/testing';
import { sha256Hex } from './text';
import { TabsModel } from './tabs';
import { UnsavedGuard, type UnsavedSource } from './unsaved';
import { ViewerSet, type LoadBuffers, type ViewerClient } from './viewer';

type CmBuffer = import('./cm/buffer').CmBuffer;

const cmBuffers: LoadBuffers = async () => {
	const m = await import('./cm/buffer');
	return { createBuffer: (o) => new m.CmBuffer(o) };
};

class Disk implements ViewerClient {
	files = new Map<string, string>();
	writes: string[] = [];
	failWrites = false;
	call = (async (name: string, p: Params<'file.read'> & Params<'file.write'>) => {
		if (name === 'file.write') {
			this.writes.push(p.path);
			if (this.failWrites) throw capError('FILE_IO_FAILED', 'disk full');
			this.files.set(p.path, p.content);
			return { path: p.path, size: p.content.length, revision: await sha256Hex(p.content) } satisfies Result<'file.write'>;
		}
		const text = this.files.get(p.path)!;
		return { path: p.path, size: text.length, modTime: 't', offset: 0, nextOffset: text.length, text, revision: await sha256Hex(text) } satisfies Result<'file.read'>;
	}) as ViewerClient['call'];
}

async function project() {
	const disk = new Disk();
	disk.files.set('a.kt', 'alpha\n');
	disk.files.set('b.kt', 'beta\n');
	const tabs = new TabsModel(null, 'w');
	const set = new ViewerSet(disk, { root: '/p' }, { dirty: (p, d) => tabs.setDirty(p, d) }, cmBuffers);
	for (const p of ['a.kt', 'b.kt']) {
		tabs.open(p);
		tabs.keep(p);
		await vi.waitFor(() => expect(set.get(p).view.state).toBe('ready'));
	}
	const guard = new UnsavedGuard();
	guard.attach(set);
	return { disk, tabs, set, guard };
}

function type(set: ViewerSet, path: string, insert: string) {
	(set.get(path).view.buffer as unknown as CmBuffer).dispatch({ changes: { from: 0, insert }, userEvent: 'input.type' });
}

/** What the tab strip's close button does (Editor.svelte). */
const close = (guard: UnsavedGuard, tabs: TabsModel, path: string) => guard.confirm(() => void tabs.close(path), [path]);

describe('UnsavedGuard', () => {
	// Decision (D7): closing a clean tab just closes; closing a dirty one asks, and Cancel keeps it with its edits.
	it('closes a clean tab at once, and asks about a dirty one: Cancel keeps the tab and the buffer', async () => {
		const { tabs, set, guard } = await project();
		type(set, 'a.kt', 'EDIT ');
		expect(tabs.view.tabs.find((t) => t.path === 'a.kt')?.dirty).toBe(true);
		expect(close(guard, tabs, 'b.kt')).toBe(true);
		expect(tabs.view.tabs.map((t) => t.path)).toEqual(['a.kt']);

		expect(close(guard, tabs, 'a.kt')).toBe(false);
		expect(guard.pending).toEqual({ paths: ['a.kt'], error: null, busy: false });
		expect(tabs.view.tabs.map((t) => t.path)).toEqual(['a.kt']); // not closed yet
		await guard.answer('cancel');
		expect(guard.pending).toBeNull();
		expect(tabs.view.tabs.map((t) => t.path)).toEqual(['a.kt']);
		expect(set.get('a.kt').view.buffer!.text()).toBe('EDIT alpha\n');
		expect(set.get('a.kt').view.dirty).toBe(true);
	});

	it('Don’t save closes the tab and drops the buffer without a write', async () => {
		const { disk, tabs, set, guard } = await project();
		type(set, 'a.kt', 'EDIT ');
		const model = set.get('a.kt');
		close(guard, tabs, 'a.kt');
		await guard.answer('discard');
		expect(tabs.view.tabs.map((t) => t.path)).toEqual(['b.kt']);
		expect(disk.writes).toEqual([]);
		expect(set.get('a.kt')).not.toBe(model);
		expect(guard.hasDirty()).toBe(false);
	});

	it('Save writes, then closes the tab', async () => {
		const { disk, tabs, set, guard } = await project();
		type(set, 'a.kt', 'EDIT ');
		close(guard, tabs, 'a.kt');
		await guard.answer('save');
		expect(disk.files.get('a.kt')).toBe('EDIT alpha\n');
		expect(tabs.view.tabs.map((t) => t.path)).toEqual(['b.kt']);
		expect(guard.pending).toBeNull();
	});

	// Decision: a save that fails keeps the question open with the reason; nothing is closed.
	it('stays open with the reason when the save fails', async () => {
		const { disk, tabs, set, guard } = await project();
		disk.failWrites = true;
		type(set, 'a.kt', 'EDIT ');
		close(guard, tabs, 'a.kt');
		await guard.answer('save');
		expect(guard.pending?.error).toContain('FILE_IO_FAILED');
		expect(guard.pending?.busy).toBe(false);
		expect(tabs.view.tabs.map((t) => t.path)).toContain('a.kt');
		await guard.answer('cancel');
		expect(set.get('a.kt').view.dirty).toBe(true);
	});

	// Decision (D7): leaving the project, and the page's unload, ask about any dirty buffer.
	it('asks about every dirty file when leaving, and knows whether any is dirty', async () => {
		const { set, guard } = await project();
		expect(guard.hasDirty()).toBe(false);
		let left = 0;
		expect(guard.confirm(() => left++)).toBe(true);
		expect(left).toBe(1);
		type(set, 'a.kt', 'X');
		type(set, 'b.kt', 'Y');
		expect(guard.hasDirty()).toBe(true);
		expect(guard.confirm(() => left++)).toBe(false);
		expect(guard.pending?.paths).toEqual(['a.kt', 'b.kt']);
		await guard.answer('cancel');
		expect(left).toBe(1);
		guard.confirm(() => left++);
		await guard.answer('save');
		expect(left).toBe(2);
		expect(guard.hasDirty()).toBe(false);
	});

	it('asks one question at a time and has nothing to ask without a project', async () => {
		const { set, guard } = await project();
		type(set, 'a.kt', 'X');
		let n = 0;
		guard.confirm(() => n++);
		expect(guard.confirm(() => (n += 10))).toBe(false);
		await guard.answer('discard');
		expect(n).toBe(1);
		const bare = new UnsavedGuard();
		expect(bare.hasDirty()).toBe(false);
		expect(bare.confirm(() => n++)).toBe(true);
	});

	it('detaches the project that went away', async () => {
		const { set } = await project();
		const guard = new UnsavedGuard();
		const source: UnsavedSource = set;
		const off = guard.attach(source);
		type(set, 'a.kt', 'X');
		expect(guard.hasDirty()).toBe(true);
		off();
		expect(guard.hasDirty()).toBe(false);
	});
});
