import { BufferGeometry, EdgesGeometry, Float32BufferAttribute } from 'three';
import { bench, describe } from 'vitest';
import { prepareSTLGeometry } from './viewerGeometry';

function geometry(triangles: number) {
  const positions = new Float32Array(triangles * 9);
  for (let i = 0; i < triangles; i++) {
    const offset = i * 9;
    positions[offset] = i;
    positions[offset + 3] = i + 1;
    positions[offset + 7] = 1;
    positions[offset + 8] = i % 2;
  }
  const result = new BufferGeometry();
  result.setAttribute('position', new Float32BufferAttribute(positions, 3));
  result.setAttribute('normal', new Float32BufferAttribute(new Float32Array(triangles * 9), 3));
  return result;
}

for (const triangles of [100_000, 1_000_000]) {
  describe(`STL preparation (${triangles} triangles)`, () => {
    const source = geometry(triangles);
    bench('previous clone and repair', () => {
      const copy = source.clone();
      copy.computeVertexNormals();
      copy.dispose();
    }, { iterations: 10, time: 0, warmupIterations: 2, warmupTime: 0 });
    bench('repair owned geometry', () => {
      prepareSTLGeometry(source);
    }, { iterations: 10, time: 0, warmupIterations: 2, warmupTime: 0 });
  });
}

describe('optional edge generation (100,001 triangles)', () => {
  const source = geometry(100_001);
  bench('previous EdgesGeometry pass', () => {
    new EdgesGeometry(source, 28).dispose();
  }, { iterations: 3, time: 0, warmupIterations: 1, warmupTime: 0 });
});
