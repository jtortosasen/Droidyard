import { describe, expect, it } from 'vitest';
import { edgeZone, type EdgeInput } from './edge';

const base: EdgeInput = {
	d: 200,
	inBand: true,
	buttons: 0,
	docked: false,
	armed: true,
	reducedMotion: false
};
const at = (d: number, over: Partial<EdgeInput> = {}) => edgeZone({ ...base, d, ...over });

describe('edgeZone', () => {
	it('is at rest beyond 96 px and attracts linearly from 96 to 24 (D44)', () => {
		expect(at(200).zone).toBe('rest');
		expect(at(97).zone).toBe('rest');
		expect(at(96)).toMatchObject({ zone: 'attract', intensity: 0 });
		expect(at(60)).toMatchObject({ zone: 'attract', intensity: 0.5 });
		expect(at(25).intensity).toBeCloseTo(0.986, 3);
	});

	it('feints from 24 px with t squared as the resistance (D44)', () => {
		expect(at(24)).toMatchObject({ zone: 'feint', intensity: 1, offset: 0 });
		// D44: a quarter of the way at half the distance; a linear ease would give 7 here.
		expect(at(14).offset).toBeCloseTo(3.5, 10);
		expect(at(5).offset).toBeCloseTo(12.635, 3);
		expect(at(5).zone).toBe('feint');
	});

	it('pops at 4 px and closer (D44)', () => {
		expect(at(4).zone).toBe('pop');
		expect(at(0).zone).toBe('pop');
	});

	it('is at rest at d = 2 when docked, a button is held, outside the band or disarmed (D44)', () => {
		for (const off of [{ docked: true }, { buttons: 1 }, { inBand: false }, { armed: false }]) {
			expect(at(2, off).zone).toBe('rest');
		}
	});

	it('draws no feint with reduced motion: attract at 1, and still pops (D44)', () => {
		expect(at(14, { reducedMotion: true })).toMatchObject({ zone: 'attract', intensity: 1, offset: 0 });
		expect(at(2, { reducedMotion: true }).zone).toBe('pop');
	});
});
