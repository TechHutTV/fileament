import { BufferGeometry, Float32BufferAttribute } from 'three';
import { describe, expect, test } from 'vitest';
import { prepareSTLGeometry } from './viewerGeometry';

describe('prepareSTLGeometry', () => {
  test('repairs missing or invalid facet normals without mutating the loader cache', () => {
    const source = new BufferGeometry();
    source.setAttribute('position', new Float32BufferAttribute([
      0, 0, 0,
      1, 0, 0,
      0, 1, 0,
    ], 3));
    source.setAttribute('normal', new Float32BufferAttribute(new Array(9).fill(0), 3));

    const prepared = prepareSTLGeometry(source);
    const repaired = prepared.getAttribute('normal');

    expect(prepared).not.toBe(source);
    expect(repaired.getZ(0)).toBeCloseTo(1);
    expect(source.getAttribute('normal').getZ(0)).toBe(0);
    prepared.dispose();
    source.dispose();
  });
});
