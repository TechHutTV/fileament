import { triangle3MF } from './testdata/triangle3mf';
import { BufferGeometry, Float32BufferAttribute, Group, Mesh, MeshPhongMaterial, MeshStandardMaterial, Texture } from 'three';
import { ThreeMFLoader } from 'three/examples/jsm/loaders/3MFLoader.js';
import { STLLoader } from 'three/examples/jsm/loaders/STLLoader.js';
import { afterEach, describe, expect, test, vi } from 'vitest';
import { loadViewerAsset, prepareViewerAsset, VIEWER_DETAIL_TRIANGLES } from './viewerResources';

const stl = 'solid part\nfacet normal 0 0 0\nouter loop\nvertex 0 0 0\nvertex 1 0 0\nvertex 0 1 0\nendloop\nendfacet\nendsolid part';
const obj = 'v 0 0 0\nv 1 0 0\nv 0 1 0\nf 1 2 3\n';

afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); });

test.each(['stl', 'obj', '3mf'] as const)('loads and releases a real %s asset', async (format) => {
  const data = format === '3mf' ? new Uint8Array(triangle3MF) : format === 'obj' ? obj : stl;
  const fetcher = vi.fn(async () => new Response(data));
  vi.stubGlobal('fetch', fetcher);
  const signal = new AbortController().signal;
  const asset = await loadViewerAsset('/mesh/model/file', format, '#c47742', signal);
  expect(asset.triangleCount).toBe(1);
  expect(fetcher).toHaveBeenCalledWith('/mesh/model/file', { credentials: 'include', signal, cache: 'no-store' });
  let mesh: Mesh | undefined;
  asset.object.traverse((object) => { if (object instanceof Mesh) mesh = object; });
  expect(mesh).toBeDefined();
  const dispose = vi.spyOn(mesh!.geometry, 'dispose');
  asset.dispose();
  asset.dispose();
  expect(dispose).toHaveBeenCalledTimes(1);
});

test('two viewers of one URL own separate geometry and materials', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => new Response(stl)));
  const left = await loadViewerAsset('/mesh/same', 'stl', '#112233', new AbortController().signal);
  const right = await loadViewerAsset('/mesh/same', 'stl', '#445566', new AbortController().signal);
  const leftMesh = left.object as Mesh<BufferGeometry, MeshStandardMaterial>;
  const rightMesh = right.object as Mesh<BufferGeometry, MeshStandardMaterial>;
  const rightGeometryDispose = vi.spyOn(rightMesh.geometry, 'dispose');
  const rightMaterialDispose = vi.spyOn(rightMesh.material, 'dispose');
  expect(leftMesh.geometry).not.toBe(rightMesh.geometry);
  expect(leftMesh.material.color.getHexString()).toBe('112233');
  expect(rightMesh.material.color.getHexString()).toBe('445566');
  left.dispose();
  expect(rightGeometryDispose).not.toHaveBeenCalled();
  expect(rightMaterialDispose).not.toHaveBeenCalled();
  expect(rightMesh.geometry.getAttribute('position').count).toBe(3);
  right.dispose();
  expect(rightGeometryDispose).toHaveBeenCalledTimes(1);
  expect(rightMaterialDispose).toHaveBeenCalledTimes(1);
});

test('deduplicates disposal of resources shared within a 3MF object', () => {
  const source = new Group();
  const geometry = new STLLoader().parse(stl);
  const texture = new Texture();
  const material = new MeshPhongMaterial({ color: '#123456', map: texture, emissiveMap: texture });
  source.add(new Mesh(geometry, material), new Mesh(geometry, material));
  const geometryDispose = vi.spyOn(geometry, 'dispose');
  const materialDispose = vi.spyOn(material, 'dispose');
  const textureDispose = vi.spyOn(texture, 'dispose');
  const asset = prepareViewerAsset(source, '3mf', '#abcdef');
  expect(asset.object).toBe(source);
  expect((source.children[0] as Mesh).material).toBe(material);
  expect(material.color.getHexString()).toBe('123456');
  expect(asset.triangleCount).toBe(2);
  asset.dispose();
  expect(geometryDispose).toHaveBeenCalledTimes(1);
  expect(materialDispose).toHaveBeenCalledTimes(1);
  expect(textureDispose).toHaveBeenCalledTimes(1);
});

test('recolors OBJ meshes and releases their replaced materials', () => {
  const source = new Group();
  const original = new MeshPhongMaterial();
  source.add(new Mesh(new STLLoader().parse(stl), original));
  const originalDispose = vi.spyOn(original, 'dispose');
  const asset = prepareViewerAsset(source, 'obj', '#c47742');
  const material = (source.children[0] as Mesh<BufferGeometry, MeshStandardMaterial>).material;
  const replacementDispose = vi.spyOn(material, 'dispose');
  expect(material).toBeInstanceOf(MeshStandardMaterial);
  expect(material.color.getHexString()).toBe('c47742');
  asset.dispose();
  expect(originalDispose).toHaveBeenCalledTimes(1);
  expect(replacementDispose).toHaveBeenCalledTimes(1);
});

test.each([VIEWER_DETAIL_TRIANGLES, VIEWER_DETAIL_TRIANGLES + 1])('sets the shadow budget for %i triangles', (triangles) => {
  const geometry = new BufferGeometry();
  geometry.setAttribute('position', new Float32BufferAttribute(new Float32Array(triangles * 9), 3));
  const asset = prepareViewerAsset(geometry, 'stl', '#112233');
  expect(asset.triangleCount).toBe(triangles);
  expect(asset.object.castShadow).toBe(triangles <= VIEWER_DETAIL_TRIANGLES);
  expect(asset.object.receiveShadow).toBe(triangles <= VIEWER_DETAIL_TRIANGLES);
  asset.dispose();
});

describe('texture loading lifecycle', () => {
  function pendingTexture() {
    const texture = new Texture();
    const geometry = new STLLoader().parse(stl);
    const material = new MeshPhongMaterial({ map: texture });
    const source = new Group();
    source.add(new Mesh(geometry, material));
    let finish: () => void = () => undefined;
    let fail: () => void = () => undefined;
    vi.spyOn(ThreeMFLoader.prototype, 'parse').mockImplementation(function (this: ThreeMFLoader) {
      this.manager.resolveURL('blob:owned-texture');
      this.manager.itemStart('texture');
      finish = () => this.manager.itemEnd('texture');
      fail = () => this.manager.itemError('texture');
      return source;
    });
    const revoke = vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => undefined);
    vi.stubGlobal('fetch', vi.fn(async () => new Response(new ArrayBuffer(0))));
    const controller = new AbortController();
    const promise = loadViewerAsset('/mesh/model/file', '3mf', '#abcdef', controller.signal);
    return { controller, promise, revoke, texture, finish: () => finish(), fail: () => fail(), source };
  }

  test('waits for textures before publishing an asset for fitting and demand rendering', async () => {
    const pending = pendingTexture();
    let resolved = false;
    void pending.promise.then(() => { resolved = true; });
    await vi.waitFor(() => expect(ThreeMFLoader.prototype.parse).toHaveBeenCalled());
    expect(resolved).toBe(false);
    pending.finish();
    const asset = await pending.promise;
    expect(asset.object).toBe(pending.source);
    asset.dispose();
    expect(pending.revoke).toHaveBeenCalledWith('blob:owned-texture');
  });

  test.each(['abort', 'error'])('releases loaded resources when texture loading ends with %s', async (mode) => {
    const pending = pendingTexture();
    const rejected = expect(pending.promise).rejects.toBeInstanceOf(Error);
    await vi.waitFor(() => expect(ThreeMFLoader.prototype.parse).toHaveBeenCalled());
    const disposed = vi.spyOn(pending.texture, 'dispose');
    if (mode === 'abort') pending.controller.abort();
    else pending.fail();
    await rejected;
    pending.finish();
    expect(disposed).toHaveBeenCalledTimes(1);
    expect(pending.revoke).toHaveBeenCalledWith('blob:owned-texture');
  });
});

test('does not parse a response after its viewer is canceled', async () => {
  const controller = new AbortController();
  vi.stubGlobal('fetch', vi.fn(async () => {
    controller.abort();
    return new Response(stl);
  }));
  const parse = vi.spyOn(STLLoader.prototype, 'parse');
  await expect(loadViewerAsset('/mesh/model/file', 'stl', '#112233', controller.signal)).rejects.toHaveProperty('name', 'AbortError');
  expect(parse).not.toHaveBeenCalled();
});

test('does not parse unsuccessful responses', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => new Response('authentication required', { status: 401 })));
  const parse = vi.spyOn(STLLoader.prototype, 'parse');
  await expect(loadViewerAsset('/mesh/model/file', 'stl', '#112233', new AbortController().signal)).rejects.toThrow('downloaded');
  expect(parse).not.toHaveBeenCalled();
});
