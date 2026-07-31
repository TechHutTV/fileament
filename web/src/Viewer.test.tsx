import { fireEvent, render, screen } from '@testing-library/react';
import '@testing-library/jest-dom/vitest';
import { createElement, forwardRef, isValidElement, useImperativeHandle, type ReactNode } from 'react';
import { BufferGeometry, Float32BufferAttribute } from 'three';
import { afterEach, describe, expect, test, vi } from 'vitest';
import ModelViewer from './Viewer';
import { prepareSTLGeometry } from './viewerGeometry';

const viewerControls = vi.hoisted(() => {
  const bounds = {} as { refresh: ReturnType<typeof vi.fn>; clip: ReturnType<typeof vi.fn>; fit: ReturnType<typeof vi.fn> };
  bounds.refresh = vi.fn(() => bounds);
  bounds.clip = vi.fn(() => bounds);
  bounds.fit = vi.fn(() => bounds);
  return {
    bounds,
    orbit: { dollyIn: vi.fn(), dollyOut: vi.fn(), update: vi.fn() },
    renderModel: false,
  };
});

vi.mock('@react-three/fiber', () => ({
  Canvas: ({ children }: { children: ReactNode }) => {
    const candidates = Array.isArray(children) ? children : [children];
    const content = candidates.find((child) => {
      if (!isValidElement<{ fallback?: ReactNode }>(child)) return false;
      return child.props.fallback === null;
    });
    return createElement('div', null, content, candidates.at(-1));
  },
  useLoader: vi.fn(() => new BufferGeometry()),
}));

vi.mock('@react-three/drei', () => {
  const Stage = ({ environment }: { environment?: string }) => createElement('div', { 'data-testid': 'remote-environment', 'data-environment': environment });
  return {
    Bounds: ({ children }: { children: ReactNode }) => {
      if (isValidElement(children) && children.type === Stage) return children;
      if (viewerControls.renderModel) return children;
      const candidates = Array.isArray(children) ? children : [children];
      const bridge = candidates.find((child) => isValidElement(child) && typeof child.type === 'function');
      return createElement('div', { 'data-testid': 'bounds' }, bridge);
    },
    Edges: (props: Record<string, unknown>) => createElement('edges', props),
    OrbitControls: forwardRef(function MockOrbitControls(_, ref) {
      useImperativeHandle(ref, () => viewerControls.orbit);
      return null;
    }),
    Stage,
    useBounds: () => viewerControls.bounds,
  };
});

afterEach(() => {
  viewerControls.bounds.refresh.mockClear();
  viewerControls.bounds.clip.mockClear();
  viewerControls.bounds.fit.mockClear();
  viewerControls.orbit.dollyIn.mockClear();
  viewerControls.orbit.dollyOut.mockClear();
  viewerControls.orbit.update.mockClear();
  viewerControls.renderModel = false;
  localStorage.removeItem('fileament-model-color');
  vi.restoreAllMocks();
});

test('uses only local viewer lighting', () => {
  render(<ModelViewer file={{ id: 'f1', modelId: 'm1', filename: 'cube.stl', relPath: 'files/cube.stl', format: 'stl', sizeBytes: 1, triangleCount: 1, bboxX: 1, bboxY: 1, bboxZ: 1 }} url="/mesh/m1/f1" />);

  expect(screen.queryByTestId('remote-environment')).not.toBeInTheDocument();
});

test('offers minimal click controls for zooming and resetting the fitted view', () => {
  render(<ModelViewer file={{ id: 'f1', modelId: 'm1', filename: 'cube.stl', relPath: 'files/cube.stl', format: 'stl', sizeBytes: 1, triangleCount: 1, bboxX: 1, bboxY: 1, bboxZ: 1 }} url="/mesh/m1/f1" />);

  fireEvent.click(screen.getByRole('button', { name: 'Zoom in' }));
  expect(viewerControls.orbit.dollyOut).toHaveBeenCalledWith(1.2);
  fireEvent.click(screen.getByRole('button', { name: 'Zoom out' }));
  expect(viewerControls.orbit.dollyIn).toHaveBeenCalledWith(1.2);
  fireEvent.click(screen.getByRole('button', { name: 'Reset view' }));
  expect(viewerControls.bounds.refresh).toHaveBeenCalled();
  expect(viewerControls.bounds.clip).toHaveBeenCalled();
  expect(viewerControls.bounds.fit).toHaveBeenCalled();
});

test('uses the saved model color for STL material and edges', () => {
  localStorage.setItem('fileament-model-color', '#c47742');
  viewerControls.renderModel = true;
  vi.spyOn(console, 'error').mockImplementation(() => undefined);
  render(<ModelViewer file={{ id: 'f1', modelId: 'm1', filename: 'cube.stl', relPath: 'files/cube.stl', format: 'stl', sizeBytes: 1, triangleCount: 1, bboxX: 1, bboxY: 1, bboxZ: 1 }} url="/mesh/m1/f1" />);

  expect(document.querySelector('meshstandardmaterial')).toHaveAttribute('color', '#c47742');
  expect(document.querySelector('edges')).toHaveAttribute('color', '#89512b');
});

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
