import { describe, expect, it } from 'vitest';
import { settle } from '../build/testing';
import { LazyEditor, type EditorHandle, type EditorModule, type EditorOptions } from './surface';

/** A handle that records its calls. */
class Fake implements EditorHandle {
	calls: string[] = [];
	constructor(readonly opts: EditorOptions) {}
	setDocument(t: string) {
		this.calls.push(`doc:${t}`);
	}
	setReadOnly(r: boolean) {
		this.calls.push(`ro:${r}`);
	}
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

/** A loader the test resolves by hand, and the mounts it made. */
function loader() {
	const mounted: Fake[] = [];
	let release!: () => void;
	let fail!: (e: unknown) => void;
	const module: EditorModule = {
		mount: (_host, opts) => {
			const f = new Fake(opts);
			mounted.push(f);
			return f;
		},
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
	// Decision (D3): the editor's code is not loaded until the editor is asked for, and what
	// was set while it loaded is what it mounts with: the latest text and flag, not the first.
	it('mounts with the last text and flag set while the code was loading', async () => {
		const l = loader();
		const ed = new LazyEditor(host, { doc: 'first', readOnly: true }, () => {}, l.load);
		expect(l.mounted).toHaveLength(0);
		ed.setDocument('second');
		ed.setReadOnly(false);
		l.release();
		await settle();
		expect(l.mounted).toHaveLength(1);
		expect(l.mounted[0]!.opts).toEqual({ doc: 'second', readOnly: false });
	});

	// Decision: a position asked for before the mount is applied after it, once.
	it('applies a reveal made before the mount, and forwards later ones', async () => {
		const l = loader();
		const ed = new LazyEditor(host, { doc: 'x', readOnly: true }, () => {}, l.load);
		ed.reveal(3, 4);
		ed.reveal(5, 6);
		l.release();
		await settle();
		ed.reveal(7, 8);
		expect(l.mounted[0]!.calls).toEqual(['reveal:5:6', 'reveal:7:8']);
	});

	// Decision: a file closed before the chunk arrived never mounts an editor on a detached host.
	it('does not mount when destroyed before the code arrives', async () => {
		const l = loader();
		const errors: unknown[] = [];
		const ed = new LazyEditor(host, { doc: 'x', readOnly: true }, (e) => errors.push(e), l.load);
		ed.destroy();
		l.release();
		await settle();
		expect(l.mounted).toHaveLength(0);
		expect(errors).toEqual([]);
	});

	it('destroys the mounted editor once and stops forwarding', async () => {
		const l = loader();
		const ed = new LazyEditor(host, { doc: 'x', readOnly: true }, () => {}, l.load);
		l.release();
		await settle();
		ed.destroy();
		ed.destroy();
		ed.setDocument('late');
		expect(l.mounted[0]!.calls).toEqual(['destroy']);
	});

	// Decision: a chunk that fails to load is reported, not swallowed (the viewer says so).
	it('reports a failed load', async () => {
		const l = loader();
		const errors: unknown[] = [];
		new LazyEditor(host, { doc: 'x', readOnly: true }, (e) => errors.push(e), l.load);
		l.fail(new Error('chunk 404'));
		await settle();
		expect(errors).toHaveLength(1);
		expect((errors[0] as Error).message).toBe('chunk 404');
	});
});
