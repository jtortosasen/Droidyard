import { describe, expect, it } from 'vitest';
import { settle } from '../build/testing';
import { LazyEditor, type EditorBuffer, type EditorHandle, type EditorModule, type EditorOptions } from './surface';

/** A handle that records its calls. */
class Fake implements EditorHandle {
	calls: string[] = [];
	constructor(readonly opts: EditorOptions) {}
	reveal(l: number, c: number) {
		this.calls.push(`reveal:${l}:${c}`);
	}
	setDiagnostics(list: readonly unknown[]) {
		this.calls.push(`diag:${list.length}`);
	}
	destroy() {
		this.calls.push('destroy');
	}
}

const buffer = {} as EditorBuffer;
const opts: EditorOptions = { buffer, onSave: () => {} };

/** A loader the test resolves by hand, and the mounts it made. */
function loader() {
	const mounted: Fake[] = [];
	let release!: () => void;
	let fail!: (e: unknown) => void;
	const module: EditorModule = {
		mount: (_host, o) => {
			const f = new Fake(o);
			mounted.push(f);
			return f;
		},
		mountCompare: async (_host, o) => {
			const f = new Fake(o);
			mounted.push(f);
			return f;
		},
		createBuffer: () => buffer,
	};
	const load = () =>
		new Promise<EditorModule>((res, rej) => {
			release = () => res(module);
			fail = rej;
		});
	return { load, mounted, release: () => release(), fail: (e: unknown) => fail(e) };
}

const host = {} as HTMLElement;

describe('LazyEditor', () => {
	// Decision (D3): the editor's code is not loaded until the editor is asked for, and the
	// buffer it mounts with is the one it was given: the state lives outside the view (D7).
	it('mounts with the buffer and the save hook it was given, once the code has loaded', async () => {
		const l = loader();
		new LazyEditor(host, opts, () => {}, l.load);
		expect(l.mounted).toHaveLength(0);
		l.release();
		await settle();
		expect(l.mounted).toHaveLength(1);
		expect(l.mounted[0]!.opts).toBe(opts);
	});

	// Decision: a position asked for before the mount is applied after it, once.
	it('applies a reveal made before the mount, and forwards later ones', async () => {
		const l = loader();
		const ed = new LazyEditor(host, opts, () => {}, l.load);
		ed.reveal(3, 4);
		ed.reveal(5, 6);
		l.release();
		await settle();
		ed.reveal(7, 8);
		expect(l.mounted[0]!.calls).toEqual(['reveal:5:6', 'reveal:7:8']);
	});

	// Decision: diagnostics set before the mount are drawn at the mount, the last list winning.
	it('applies the last diagnostics set before the mount', async () => {
		const l = loader();
		const ed = new LazyEditor(host, opts, () => {}, l.load);
		ed.setDiagnostics([{ line: 1, col: 1, severity: 'error', message: 'a' }]);
		ed.setDiagnostics([
			{ line: 1, col: 1, severity: 'error', message: 'a' },
			{ line: 2, col: 1, severity: 'error', message: 'b' },
		]);
		l.release();
		await settle();
		expect(l.mounted[0]!.calls).toEqual(['diag:2']);
	});

	// Decision: an empty list set before the mount is still applied (it clears a buffer's older marks).
	it('applies an empty diagnostics list at the mount', async () => {
		const l = loader();
		const ed = new LazyEditor(host, opts, () => {}, l.load);
		ed.setDiagnostics([]);
		l.release();
		await settle();
		expect(l.mounted[0]!.calls).toEqual(['diag:0']);
	});

	// Decision: a file closed before the chunk arrived never mounts an editor on a detached host.
	it('does not mount when destroyed before the code arrives', async () => {
		const l = loader();
		const errors: unknown[] = [];
		const ed = new LazyEditor(host, opts, (e) => errors.push(e), l.load);
		ed.destroy();
		l.release();
		await settle();
		expect(l.mounted).toHaveLength(0);
		expect(errors).toEqual([]);
	});

	it('destroys the mounted editor once and stops forwarding', async () => {
		const l = loader();
		const ed = new LazyEditor(host, opts, () => {}, l.load);
		l.release();
		await settle();
		ed.destroy();
		ed.destroy();
		ed.reveal(1, 1);
		expect(l.mounted[0]!.calls).toEqual(['destroy']);
	});

	// Decision (D8): Compare's code is a further chunk, so its mount is asynchronous; a file closed
	// while it arrives must not leave a mounted view behind, and one that stays gets its calls.
	it('destroys a Compare view that arrives after the editor was closed, and keeps one that does not', async () => {
		const l = loader();
		let arrive!: () => void;
		const late = new Fake(opts);
		const pending = new Promise<EditorHandle>((r) => (arrive = () => r(late)));
		const ed = new LazyEditor(host, opts, () => {}, l.load, () => pending);
		l.release();
		await settle();
		ed.destroy(); // closed while the mount is still arriving
		arrive();
		await settle();
		expect(late.calls).toEqual(['destroy']);

		const l2 = loader();
		const kept = new Fake(opts);
		const ed2 = new LazyEditor(host, opts, () => {}, l2.load, async () => kept);
		ed2.reveal(2, 3);
		l2.release();
		await settle();
		expect(kept.calls).toEqual(['reveal:2:3']);
	});

	// Decision: a chunk that fails to load is reported, not swallowed (the viewer says so).
	it('reports a failed load', async () => {
		const l = loader();
		const errors: unknown[] = [];
		new LazyEditor(host, opts, (e) => errors.push(e), l.load);
		l.fail(new Error('chunk 404'));
		await settle();
		expect(errors).toHaveLength(1);
		expect((errors[0] as Error).message).toBe('chunk 404');
	});
});
