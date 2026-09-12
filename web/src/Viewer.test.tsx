import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import '@testing-library/jest-dom/vitest';
import { Children, createElement, forwardRef, isValidElement, useImperativeHandle, type ReactNode } from 'react';
import { BufferGeometry, Float32BufferAttribute, Mesh, MeshStandardMaterial } from 'three';
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest';
import ModelViewer from './Viewer';
import { prepareSTLGeometry } from './viewerGeometry';
import { ViewerBoundary } from './ViewerBoundary';

const stl = 'solid part\nfacet normal 0 0 0\nouter loop\nvertex 0 0 0\nvertex 1 0 0\nvertex 0 1 0\nendloop\nendfacet\nendsolid part';
const file = { id: 'f1', modelId: 'm1', filename: 'cube.stl', relPath: 'files/cube.stl', format: 'stl' as const, sizeBytes: 1, triangleCount: 1, bboxX: 1, bboxY: 1, bboxZ: 1 };
const viewerControls = vi.hoisted(() => {
  const bounds = {} as { refresh: ReturnType<typeof vi.fn>; clip: ReturnType<typeof vi.fn>; fit: ReturnType<typeof vi.fn> };
  bounds.refresh = vi.fn(() => bounds);
  bounds.clip = vi.fn(() => bounds);
  bounds.fit = vi.fn(() => bounds);
  return { bounds, orbit: { dollyIn: vi.fn(), dollyOut: vi.fn(), update: vi.fn() } };
});

function sceneNodes(children: ReactNode): ReactNode {
  return Children.map(children, (child) => {
    if (!isValidElement<{ children?: ReactNode; object?: Mesh<BufferGeometry, MeshStandardMaterial> }>(child)) return child;
    if (child.type === 'primitive') return createElement('div', { 'data-testid': 'mesh', 'data-color': child.props.object?.material.color.getHexString() }, sceneNodes(child.props.children));
    if (typeof child.type === 'string') return createElement('div', null, sceneNodes(child.props.children));
    return child;
  });
}

vi.mock('@react-three/fiber', () => ({
  Canvas: ({ children, frameloop, shadows }: { children: ReactNode; frameloop?: string; shadows?: boolean }) => createElement('div', { 'data-testid': 'canvas', 'data-frameloop': frameloop, 'data-shadows': String(shadows) }, Children.toArray(children).filter((child) => isValidElement(child) && typeof child.type !== 'string')),
}));

vi.mock('@react-three/drei', () => ({
  Bounds: ({ children }: { children: ReactNode }) => createElement('div', { 'data-testid': 'bounds' }, sceneNodes(children)),
  Edges: ({ color }: { color: string }) => createElement('div', { 'data-testid': 'edges', 'data-color': color }),
  OrbitControls: forwardRef(function MockOrbitControls({ enableDamping }: { enableDamping?: boolean }, ref) {
    useImperativeHandle(ref, () => viewerControls.orbit);
    return createElement('div', { 'data-testid': 'orbit', 'data-damping': String(enableDamping) });
  }),
  useBounds: () => viewerControls.bounds,
}));

beforeEach(() => vi.stubGlobal('fetch', vi.fn(async () => new Response(stl))));
afterEach(() => {
  vi.clearAllMocks();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  localStorage.removeItem('fileament-model-color');
});

test('renders on demand while preserving camera damping', async () => {
  render(<ModelViewer file={file} url="/mesh/m1/f1" />);
  expect(await screen.findByTestId('mesh')).toBeInTheDocument();
  expect(screen.getByTestId('canvas')).toHaveAttribute('data-frameloop', 'demand');
  expect(screen.getByTestId('orbit')).toHaveAttribute('data-damping', 'true');
  expect(fetch).toHaveBeenCalledTimes(1);
  expect(fetch).toHaveBeenCalledWith('/mesh/m1/f1', expect.objectContaining({ credentials: 'include' }));
});

test('shows loading progress before fitting the downloaded geometry', async () => {
  let finish: () => void = () => undefined;
  vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>((resolve) => { finish = () => resolve(new Response(stl)); })));
  render(<ModelViewer file={file} url="/mesh/m1/f1" />);
  expect(screen.getByRole('status')).toHaveTextContent('Loading 3D view');
  expect(screen.queryByTestId('bounds')).not.toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Reset view' })).toBeDisabled();
  finish();
  expect(await screen.findByTestId('bounds')).toBeInTheDocument();
  await waitFor(() => expect(screen.queryByRole('status')).not.toBeInTheDocument());
});

test('offers click controls for zooming and resetting the fitted view', async () => {
  render(<ModelViewer file={file} url="/mesh/m1/f1" />);
  await waitFor(() => expect(screen.getByRole('button', { name: 'Zoom in' })).toBeEnabled());
  fireEvent.click(screen.getByRole('button', { name: 'Zoom in' }));
  expect(viewerControls.orbit.dollyOut).toHaveBeenCalledWith(1.2);
  fireEvent.click(screen.getByRole('button', { name: 'Zoom out' }));
  expect(viewerControls.orbit.dollyIn).toHaveBeenCalledWith(1.2);
  expect(viewerControls.orbit.update).toHaveBeenCalledTimes(2);
  fireEvent.click(screen.getByRole('button', { name: 'Reset view' }));
  expect(viewerControls.bounds.refresh).toHaveBeenCalled();
  expect(viewerControls.bounds.clip).toHaveBeenCalled();
  expect(viewerControls.bounds.fit).toHaveBeenCalled();
});

test('uses the saved model color for STL material and edges', async () => {
  localStorage.setItem('fileament-model-color', '#c47742');
  render(<ModelViewer file={file} url="/mesh/m1/f1" />);
  expect(await screen.findByTestId('mesh')).toHaveAttribute('data-color', 'c47742');
  expect(screen.getByTestId('edges')).toHaveAttribute('data-color', '#89512b');
  await waitFor(() => expect(screen.getByTestId('canvas')).toHaveAttribute('data-shadows', 'true'));
});

test('omits expensive edge and shadow passes for a complex mesh', async () => {
  const bytes = new ArrayBuffer(84 + 50 * 100_001);
  new DataView(bytes).setUint32(80, 100_001, true);
  vi.stubGlobal('fetch', vi.fn(async () => new Response(bytes)));
  render(<ModelViewer file={{ ...file, triangleCount: 100_001 }} url="/mesh/m1/complex" />);
  expect(await screen.findByTestId('mesh')).toBeInTheDocument();
  expect(screen.queryByTestId('edges')).not.toBeInTheDocument();
  expect(screen.getByTestId('canvas')).toHaveAttribute('data-shadows', 'false');
});

test('contains download failures inside the viewer boundary', async () => {
  vi.spyOn(console, 'error').mockImplementation(() => undefined);
  vi.stubGlobal('fetch', vi.fn(async () => new Response('unavailable', { status: 503 })));
  render(<ViewerBoundary><ModelViewer file={file} url="/mesh/m1/f1" /></ViewerBoundary>);
  expect(await screen.findByRole('alert')).toHaveTextContent('Downloads are still available');
  expect(screen.queryByTestId('canvas')).not.toBeInTheDocument();
});

describe('prepareSTLGeometry', () => {
  test('repairs facet normals in the owned geometry without copying its position buffer', () => {
    const source = new BufferGeometry();
    source.setAttribute('position', new Float32BufferAttribute([0, 0, 0, 1, 0, 0, 0, 1, 0], 3));
    source.setAttribute('normal', new Float32BufferAttribute(new Array(9).fill(0), 3));
    const positions = source.getAttribute('position').array;
    const prepared = prepareSTLGeometry(source);
    expect(prepared).toBe(source);
    expect(prepared.getAttribute('position').array).toBe(positions);
    expect(prepared.getAttribute('normal').getZ(0)).toBeCloseTo(1);
    prepared.dispose();
  });
});
