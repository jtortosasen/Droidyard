// Joins the project file to the models that take their selection from it (ADR-0046,
// D19, D20). Two directions, kept apart:
//   - once the file has been read (or found absent, or left alone), its values preselect:
//     each model applies one only if its own daemon-fed list holds it, and not at all once
//     the user has chosen. Nothing is written for this;
//   - each choice the user makes (module and variant, a device, a started AVD, the
//     application id an install reports) becomes one change to the file.
// The file never causes a call to the daemon beyond its own `file.*`.
import type { BuildModel } from '../build/build';
import type { DevicesModel } from '../devices/devices';
import type { EmulatorsModel } from '../devices/emulators';
import type { InstallModel } from '../devices/install';
import { devicePrefOf, validApplicationId, type ProjectFileModel } from './projectfile';

export interface ProjectModels {
	file: ProjectFileModel;
	build: BuildModel;
	devices: DevicesModel;
	emulators: EmulatorsModel;
	install: InstallModel;
}

export function bindProjectFile(m: ProjectModels): () => void {
	let applied = false;
	const offs: (() => void)[] = [];

	// The file is read once per tab, so it preselects once.
	const preselect = () => {
		const v = m.file.view;
		if (applied || v.state === 'idle' || v.state === 'loading') return;
		applied = true;
		const { build, device, applicationId } = v.prefs;
		m.build.setPreselect(build ?? null);
		m.devices.setPreselect(device ?? null);
		m.emulators.setPreselect(device && 'avd' in device ? device.avd : null);
		if (applicationId !== undefined) m.install.setStoredApplicationId(applicationId);
	};
	offs.push(m.file.subscribe(preselect));
	preselect();
	// D36 with ADR-0046 §3: the file's AVD is the toolbar's pending AVD only while
	// `emulator.list` lists it as not running (the emulators model's `preselected`).
	const restoreAvd = () => {
		const avd = m.emulators.view.preselected;
		if (avd !== '') m.devices.restoreAvd(avd);
	};
	offs.push(m.emulators.subscribe(restoreAvd));
	restoreAvd();

	offs.push(m.build.subscribeChoice((build) => m.file.update({ build })));
	offs.push(
		m.devices.subscribeChoice((serial) => {
			const d = m.devices.view.devices.find((x) => x.device === serial);
			const device = d ? devicePrefOf(d) : null;
			if (device !== null) m.file.update({ device });
		}),
	);
	// D36: an AVD picked in the toolbar and not yet running is the file's {avd} at once.
	offs.push(m.devices.subscribeAvdChoice((avd) => m.file.update({ device: { avd } })));
	offs.push(m.emulators.subscribeChoice((avd) => m.file.update({ device: { avd } })));
	offs.push(
		m.install.subscribeInstalled((applicationId) => {
			// The daemon's text is checked like the file's before it is stored.
			if (validApplicationId(applicationId)) m.file.update({ applicationId });
		}),
	);
	return () => {
		for (const off of offs) off();
	};
}
