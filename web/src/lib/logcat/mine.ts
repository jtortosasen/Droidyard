// "mine" (plan m2e D39): the application ids this tab knows for this workspace, which
// `package:mine` matches. It is the union of every build output the tab has seen (the last
// result at load and after each build) and of its installs. The project file's id is not
// here: ADR-0046 §3 uses it for logs only after a click. `androidTest` APKs are not build
// outputs (ADR-0023 §6), so test apps never count. Ids are only added: a rename in the
// build keeps the old id, whose lines are still in the panel.

export interface MineBuild {
	view: { last: { outputs?: readonly { applicationId?: string }[] } | null };
	subscribe(listener: () => void): () => void;
}

export interface MineInstall {
	subscribeInstalled(fn: (applicationId: string) => void): () => void;
}

export class MineSet {
	#ids = new Set<string>();
	#listeners = new Set<() => void>();
	#offs: (() => void)[];
	#build: MineBuild;

	constructor(build: MineBuild, install: MineInstall) {
		this.#build = build;
		this.#offs = [build.subscribe(() => this.#fromBuild()), install.subscribeInstalled((id) => this.#add([id]))];
		this.#fromBuild();
	}

	/** The ids known now. The set is live: a query holds it and reads it at every test. */
	get ids(): ReadonlySet<string> {
		return this.#ids;
	}

	/** `fn` runs when an id joins the set. */
	subscribe(fn: () => void): () => void {
		this.#listeners.add(fn);
		return () => {
			this.#listeners.delete(fn);
		};
	}

	dispose(): void {
		for (const off of this.#offs) off();
		this.#offs = [];
		this.#listeners.clear();
	}

	#fromBuild(): void {
		const outputs = this.#build.view.last?.outputs ?? [];
		this.#add(outputs.map((o) => o.applicationId ?? ''));
	}

	#add(ids: readonly string[]): void {
		let grew = false;
		for (const id of ids) {
			if (id !== '' && !this.#ids.has(id)) {
				this.#ids.add(id);
				grew = true;
			}
		}
		if (grew) for (const fn of [...this.#listeners]) fn();
	}
}
