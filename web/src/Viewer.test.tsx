import { render, screen } from '@testing-library/react';
import '@testing-library/jest-dom/vitest';
import { createElement, isValidElement, type ReactNode } from 'react';
import { BufferGeometry, Float32BufferAttribute } from 'three';
import { describe, expect, test, vi } from 'vitest';
import ModelViewer from './Viewer';
import { prepareSTLGeometry } from './viewerGeometry';

vi.mock('@react-three/fiber', () => ({
  Canvas: ({ children }: { children: ReactNode }) => {
    const candidates = Array.isArray(children) ? children : [children];
    return candidates.find((child) => {
      if (!isValidElement<{ fallback?: ReactNode }>(child)) return false;
      return child.props.fallback === null;
    }) ?? null;
  },
  useLoader: vi.fn(),
}));

vi.mock('@react-three/drei', () => {
  const Stage = ({ environment }: { environment?: string }) => createElement('div', { 'data-testid': 'remote-environment', 'data-environment': environment });
  return {
    Bounds: ({ children }: { children: ReactNode }) => isValidElement(children) && children.type === Stage ? children : createElement('div', { 'data-testid': 'bounds' }),
    Edges: () => null,
    OrbitControls: () => null,
    Stage,
  };
});

test('uses only local viewer lighting', () => {
  render(<ModelViewer file={{ id: 'f1', modelId: 'm1', filename: 'cube.stl', relPath: 'files/cube.stl', format: 'stl', sizeBytes: 1, triangleCount: 1, bboxX: 1, bboxY: 1, bboxZ: 1 }} url="/mesh/m1/f1" />);

  expect(screen.queryByTestId('remote-environment')).not.toBeInTheDocument();
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
