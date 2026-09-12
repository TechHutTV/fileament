import type { BufferGeometry } from 'three';

export function prepareSTLGeometry(geometry: BufferGeometry) {
  geometry.computeVertexNormals();
  return geometry;
}
