import type { BufferGeometry } from 'three';

export function prepareSTLGeometry(source: BufferGeometry) {
  const geometry = source.clone();
  geometry.computeVertexNormals();
  return geometry;
}
