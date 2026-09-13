import { memoryUsage } from 'node:process';
import { table } from 'node:console';
import { BufferGeometry, Float32BufferAttribute } from 'three';
import { prepareViewerAsset } from '../src/viewerResources.ts';

if (typeof globalThis.gc !== 'function') throw new Error('Run the bundled benchmark with node --expose-gc');

function geometry() {
  const source = new BufferGeometry();
  const positions = new Float32Array(100_000 * 9);
  for (let offset = 0; offset < positions.length; offset += 9) {
    positions[offset + 3] = 1;
    positions[offset + 7] = 1;
  }
  source.setAttribute('position', new Float32BufferAttribute(positions, 3));
  source.setAttribute('normal', new Float32BufferAttribute(new Float32Array(positions.length), 3));
  return source;
}

function retainedBytes() {
  globalThis.gc();
  return memoryUsage().arrayBuffers;
}

function previousCache() {
  const cache = [];
  let current;
  for (let i = 0; i < 20; i++) {
    current?.dispose();
    const source = geometry();
    cache.push(source);
    current = source.clone();
    current.computeVertexNormals();
  }
  return () => {
    current.dispose();
    current = undefined;
    cache.length = 0;
  };
}

function ownedAssets() {
  let current;
  for (let i = 0; i < 20; i++) {
    current?.dispose();
    current = prepareViewerAsset(geometry(), 'stl', '#4f9f88');
  }
  return () => {
    current.dispose();
    current = undefined;
  };
}

function measure(build) {
  const initial = retainedBytes();
  const release = build();
  const retained = retainedBytes() - initial;
  release();
  return { retainedArrayBufferBytes: retained, afterReleaseBytes: retainedBytes() - initial };
}

table({ previousCache: measure(previousCache), ownedAssets: measure(ownedAssets) });
