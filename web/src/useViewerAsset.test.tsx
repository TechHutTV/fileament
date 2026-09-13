import { act, renderHook } from '@testing-library/react';
import { StrictMode } from 'react';
import { Mesh } from 'three';
import { STLLoader } from 'three/examples/jsm/loaders/STLLoader.js';
import { afterEach, expect, test, vi } from 'vitest';
import { useViewerAsset } from './useViewerAsset';
import * as resources from './viewerResources';

const stl = 'solid part\nfacet normal 0 0 0\nouter loop\nvertex 0 0 0\nvertex 1 0 0\nvertex 0 1 0\nendloop\nendfacet\nendsolid part';
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); });

function pendingLoads() {
  const requests: { signal: AbortSignal; resolve: (asset: resources.ViewerAsset) => void }[] = [];
  vi.spyOn(resources, 'loadViewerAsset').mockImplementation((_url, _format, _color, signal) => new Promise((resolve) => { requests.push({ signal, resolve }); }));
  return requests;
}

function model() {
  const asset = resources.prepareViewerAsset(new STLLoader().parse(stl), 'stl', '#112233');
  const disposed = vi.spyOn((asset.object as Mesh).geometry, 'dispose');
  return { asset, disposed };
}

test('cancels the previous request and disposes late results after changing variants', async () => {
  const requests = pendingLoads();
  const { result, rerender, unmount } = renderHook(({ url }) => useViewerAsset(url, 'stl', '#112233'), { initialProps: { url: '/first' } });
  rerender({ url: '/second' });
  expect(requests[0].signal.aborted).toBe(true);
  expect(result.current.asset).toBeUndefined();
  const old = model();
  await act(async () => requests[0].resolve(old.asset));
  expect(old.disposed).toHaveBeenCalledTimes(1);
  expect(result.current.asset).toBeUndefined();
  const current = model();
  await act(async () => requests[1].resolve(current.asset));
  expect(result.current.asset).toBe(current.asset);
  expect(current.disposed).not.toHaveBeenCalled();
  unmount();
  expect(current.disposed).toHaveBeenCalledTimes(1);
});

test('never disposes a live Strict Mode replacement when an abandoned load finishes', async () => {
  const requests = pendingLoads();
  const { result, unmount } = renderHook(() => useViewerAsset('/same', 'stl', '#112233'), { wrapper: StrictMode });
  expect(requests).toHaveLength(2);
  expect(requests[0].signal.aborted).toBe(true);
  const abandoned = model();
  const live = model();
  await act(async () => { requests[1].resolve(live.asset); requests[0].resolve(abandoned.asset); });
  expect(result.current.asset).toBe(live.asset);
  expect(abandoned.disposed).toHaveBeenCalledTimes(1);
  expect(live.disposed).not.toHaveBeenCalled();
  unmount();
  expect(live.disposed).toHaveBeenCalledTimes(1);
});

test('releases every previous geometry across repeated variant changes', async () => {
  const requests = pendingLoads();
  const { rerender, unmount } = renderHook(({ url }) => useViewerAsset(url, 'stl', '#112233'), { initialProps: { url: '/0' } });
  const models = [];
  for (let i = 0; i < 25; i++) {
    if (i) rerender({ url: '/'+i });
    const next = model();
    models.push(next);
    await act(async () => requests[i].resolve(next.asset));
    expect(models.filter((entry) => entry.disposed.mock.calls.length === 0)).toHaveLength(1);
  }
  unmount();
  for (const entry of models) expect(entry.disposed).toHaveBeenCalledTimes(1);
});
