import { BufferGeometry, LoadingManager, Material, Mesh, MeshStandardMaterial, Object3D, Texture } from 'three';
import { STLLoader } from 'three/examples/jsm/loaders/STLLoader.js';
import { OBJLoader } from 'three/examples/jsm/loaders/OBJLoader.js';
import { ThreeMFLoader } from 'three/examples/jsm/loaders/3MFLoader.js';
import type { ModelFile } from './App';
import { prepareSTLGeometry } from './viewerGeometry';

export const VIEWER_DETAIL_TRIANGLES = 100_000;
export type ViewerAsset = {
  object: Object3D;
  stlGeometry?: BufferGeometry;
  triangleCount: number;
  dispose: () => void;
};

// Each parsed object belongs to one viewer; nothing is stored in useLoader's global cache.
export function prepareViewerAsset(source: BufferGeometry | Object3D, format: ModelFile['format'], color: string, objectURLs = new Set<string>()): ViewerAsset {
  const replacement = format === '3mf' ? undefined : new MeshStandardMaterial({ color, roughness: 0.48, metalness: 0.03 });
  const geometry = source instanceof BufferGeometry ? prepareSTLGeometry(source) : undefined;
  const object = geometry ? new Mesh(geometry, replacement) : source as Object3D;
  const resources = new Set<BufferGeometry | Material | Texture>();
  if (replacement) resources.add(replacement);
  let triangleCount = 0;
  object.traverse((child) => {
    if ('geometry' in child && child.geometry instanceof BufferGeometry) resources.add(child.geometry);
    if ('material' in child) {
      const materials = Array.isArray(child.material) ? child.material : [child.material];
      for (const material of materials) {
        if (!(material instanceof Material)) continue;
        resources.add(material);
        for (const value of Object.values(material)) {
          if (value instanceof Texture) resources.add(value);
        }
      }
    }
    if (child instanceof Mesh) {
      triangleCount += Math.floor((child.geometry.index?.count ?? child.geometry.getAttribute('position')?.count ?? 0) / 3);
      if (format === 'obj' && replacement) child.material = replacement;
    }
  });
  const detailed = triangleCount > 0 && triangleCount <= VIEWER_DETAIL_TRIANGLES;
  object.traverse((child) => {
    if (child instanceof Mesh) {
      child.castShadow = detailed;
      child.receiveShadow = detailed;
    }
  });
  return {
    object, stlGeometry: geometry, triangleCount,
    dispose: () => {
      for (const resource of resources) resource.dispose();
      resources.clear();
      for (const url of objectURLs) URL.revokeObjectURL(url);
      objectURLs.clear();
    },
  };
}

export async function loadViewerAsset(url: string, format: ModelFile['format'], color: string, signal: AbortSignal): Promise<ViewerAsset> {
  const response = await fetch(url, { credentials: 'include', signal, cache: 'no-store' });
  if (!response.ok) throw new Error('The model could not be downloaded');
  const data = format === 'obj' ? await response.text() : await response.arrayBuffer();
  signal.throwIfAborted();
  return new Promise((resolve, reject) => {
    let asset: ViewerAsset | undefined;
    let finished = false;
    const objectURLs = new Set<string>();
    const manager = new LoadingManager();
    manager.setURLModifier((value) => {
      if (value.startsWith('blob:')) objectURLs.add(value);
      return value;
    });
    const fail = (error: unknown) => {
      if (finished) return;
      finished = true;
      signal.removeEventListener('abort', abort);
      asset?.dispose();
      for (const value of objectURLs) URL.revokeObjectURL(value);
      objectURLs.clear();
      reject(error instanceof Error ? error : new Error('The model could not be loaded'));
    };
    const abort = () => fail(new DOMException('Model loading canceled', 'AbortError'));
    manager.onError = () => fail(new Error('A model texture could not be loaded'));
    manager.onLoad = () => {
      if (finished || !asset) return;
      finished = true;
      signal.removeEventListener('abort', abort);
      resolve(asset);
    };
    signal.addEventListener('abort', abort, { once: true });
    manager.itemStart('model');
    try {
      const source = format === 'obj'
        ? new OBJLoader(manager).parse(data as string)
        : format === '3mf'
          ? new ThreeMFLoader(manager).parse(data as ArrayBuffer)
          : new STLLoader(manager).parse(data as ArrayBuffer);
      asset = prepareViewerAsset(source, format, color, objectURLs);
      if (finished) asset.dispose();
      manager.itemEnd('model');
    } catch (error) {
      fail(error);
    }
  });
}
