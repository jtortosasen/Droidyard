// The viscous left edge (m2f D44): a pure function of the pointer's distance `d` to the
// window's left edge. Far away nothing; from 96 px the sessions toggle is attracted; from
// 24 px the overlay's edge is feinted out, with resistance (a quarter of the way at half
// the distance); at 4 px it pops. The inputs that switch it off (D44: column docked, a
// button held, the pointer outside the band, disarmed after a hide or an undock) arrive
// as flags: this function holds no state.

export const ATTRACT_PX = 96;
export const FEINT_PX = 24;
export const POP_PX = 4;
export const FEINT_MAX_PX = 14;

export type EdgeZone = 'rest' | 'attract' | 'feint' | 'pop';

export interface EdgeInput {
	/** The pointer's clientX: its distance to the left edge. */
	d: number;
	/** The pointer's clientY lies in the band (toolbar top to the middle's bottom, no modal dialog). */
	inBand: boolean;
	/** `PointerEvent.buttons`: any held button switches the edge off. */
	buttons: number;
	/** The sessions column is docked: no edge. */
	docked: boolean;
	/** False after a hide or an undock until the pointer has been at d > FEINT_PX (D44). */
	armed: boolean;
	reducedMotion: boolean;
}

export interface Edge {
	zone: EdgeZone;
	/** 0..1: the toggle's `--attract`. */
	intensity: number;
	/** px the overlay's edge is out in the feint; 0 elsewhere. */
	offset: number;
}

const REST: Edge = { zone: 'rest', intensity: 0, offset: 0 };

export function edgeZone(x: EdgeInput): Edge {
	if (x.docked || x.buttons !== 0 || !x.inBand || x.d > ATTRACT_PX) return REST;
	if (x.d > FEINT_PX) {
		// D44: linear from 0 at 96 px to 1 at 24 px.
		return { zone: 'attract', intensity: (ATTRACT_PX - x.d) / (ATTRACT_PX - FEINT_PX), offset: 0 };
	}
	// D44: a disarmed edge draws no feint and no pop inside the feint zone.
	if (!x.armed) return REST;
	if (x.d <= POP_PX) return { zone: 'pop', intensity: 1, offset: 0 };
	// D44: reduced motion has no feint; the zone reads as attract at 1.
	if (x.reducedMotion) return { zone: 'attract', intensity: 1, offset: 0 };
	const t = (FEINT_PX - x.d) / (FEINT_PX - POP_PX);
	// D44: t squared is the resistance; a linear ease would give 7 at half the distance.
	return { zone: 'feint', intensity: 1, offset: FEINT_MAX_PX * t * t };
}
