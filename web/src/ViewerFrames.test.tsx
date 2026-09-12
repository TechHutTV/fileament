import { OrbitControls, type BoundsApi } from '@react-three/drei';
import { act, createRoot, extend } from '@react-three/fiber';
import { createRef, type ComponentRef } from 'react';
import * as THREE from 'three';
import { afterEach, expect, test, vi } from 'vitest';
import { ViewerScene } from './Viewer';

extend({ Group: THREE.Group });
afterEach(() => { vi.useRealTimers(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

test('real scene fitting and camera movement settle back to zero scheduled frames', async () => {
  vi.useFakeTimers();
  const canvas = document.createElement('canvas');
  const render = vi.fn();
  const gl = {
    domElement: canvas, render, setPixelRatio: vi.fn(), setSize: vi.fn(),
    shadowMap: { enabled: false },
    xr: { isPresenting: false, addEventListener: vi.fn(), removeEventListener: vi.fn() },
    renderLists: { dispose: vi.fn() }, forceContextLoss: vi.fn(),
  } as unknown as THREE.WebGLRenderer;
  const root = createRoot(canvas);
  const bounds = { current: null as BoundsApi | null };
  const controls = createRef<ComponentRef<typeof OrbitControls>>();
  let ready = false;
  const scene = (url: string) => <><ViewerScene url={url} format="stl" color="#4f9f88" edgeColor="#224433" bounds={bounds} onReady={(model) => { ready = !!model; }} /><OrbitControls ref={controls} makeDefault enableDamping dampingFactor={0.08} minPolarAngle={0.12} maxPolarAngle={Math.PI / 2.05} /></>;
  vi.stubGlobal('fetch', vi.fn(async () => new Response('solid p\nfacet normal 0 0 1\nouter loop\nvertex 0 0 0\nvertex 1 0 0\nvertex 0 1 0\nendloop\nendfacet\nendsolid p')));
  await root.configure({ gl, frameloop: 'demand', size: { width: 800, height: 600, top: 0, left: 0 }, camera: { position: [4, 3.2, 4], fov: 32 } });
  try {
    await act(async () => {
      root.render(scene('/mesh/test'));
    });
    await act(async () => { await vi.advanceTimersByTimeAsync(3000); });
    expect(ready).toBe(true);
    expect(render).toHaveBeenCalled();
    expect(bounds.current).not.toBeNull();
    const fitted = render.mock.calls.length;
    await act(async () => { await vi.advanceTimersByTimeAsync(1000); });
    expect(render).toHaveBeenCalledTimes(fitted);

    const camera = controls.current!.object;
    const before = camera.position.clone();
    await act(async () => {
      controls.current!.dollyOut(1.2);
      controls.current!.update();
      await vi.advanceTimersByTimeAsync(2000);
    });
    expect(camera.position.distanceTo(before)).toBeGreaterThan(0);
    expect(render.mock.calls.length).toBeGreaterThan(fitted);
    const zoomed = render.mock.calls.length;
    await act(async () => { await vi.advanceTimersByTimeAsync(1000); });
    expect(render).toHaveBeenCalledTimes(zoomed);

    const angle = controls.current!.getAzimuthalAngle();
    await act(async () => {
      controls.current!.setAzimuthalAngle(angle + 0.5);
      await vi.advanceTimersByTimeAsync(100);
    });
    expect(controls.current!.getAzimuthalAngle()).toBeGreaterThan(angle);
    expect(controls.current!.getAzimuthalAngle()).toBeLessThan(angle + 0.5);
    expect(render.mock.calls.length).toBeGreaterThan(zoomed + 1);
    await act(async () => { await vi.advanceTimersByTimeAsync(3000); });
    const rotated = render.mock.calls.length;
    await act(async () => { await vi.advanceTimersByTimeAsync(1000); });
    expect(render).toHaveBeenCalledTimes(rotated);

    await act(async () => {
      bounds.current!.refresh().clip().fit();
      await vi.advanceTimersByTimeAsync(3000);
    });
    expect(render.mock.calls.length).toBeGreaterThan(rotated);
    const reset = render.mock.calls.length;
    await act(async () => { await vi.advanceTimersByTimeAsync(1000); });
    expect(render).toHaveBeenCalledTimes(reset);

    const renderedScene = render.mock.calls.at(-1)![0] as THREE.Scene;
    let mesh: THREE.Mesh | undefined;
    renderedScene.traverse((child) => {
      if (child instanceof THREE.Mesh && child.geometry.getAttribute('position')?.count === 3) mesh = child;
    });
    expect(mesh).toBeDefined();
    let disposed = false;
    mesh!.geometry.addEventListener('dispose', () => {
      expect(renderedScene.getObjectById(mesh!.id)).toBeUndefined();
      disposed = true;
    });
    await act(async () => root.render(scene('/mesh/next')));
    await act(async () => { await vi.advanceTimersByTimeAsync(3000); });
    expect(disposed).toBe(true);
    expect(ready).toBe(true);
  } finally {
    await act(async () => root.unmount());
    await vi.advanceTimersByTimeAsync(600);
  }
});
