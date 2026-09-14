import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import '@testing-library/jest-dom/vitest';
import { afterEach, expect, test, vi } from 'vitest';
import { App } from './App';

vi.mock('./Viewer', () => ({ default: ({ url }: { url: string }) => <div aria-label="3D viewer">{url}</div> }));

afterEach(() => vi.unstubAllGlobals());

for (const scope of ['owner', 'public']) {
  test.each([
    { label: 'complex small mesh', triangleCount: 250_001, sizeBytes: 1024 },
    { label: 'unknown geometry', triangleCount: null, sizeBytes: 1024 },
    { label: 'large simple mesh', triangleCount: 12, sizeBytes: 50 * 1024 * 1024 + 1 },
  ])(`${scope}: $label requires manual viewer loading for each variant`, async ({ triangleCount, sizeBytes }) => {
    setup(scope, triangleCount, sizeBytes);
    fireEvent.click(await screen.findByRole('button', { name: 'Load 3D view' }));
    expect(await screen.findByLabelText('3D viewer')).toHaveTextContent(scope === 'owner' ? '/mesh/m1/f1' : '/api/public/token/mesh/f1');
    if (scope === 'owner') {
      fireEvent.click(screen.getByRole('button', { name: /Choose variant/ }));
      fireEvent.click(screen.getByRole('menuitemradio', { name: /second.stl/ }));
    } else {
      fireEvent.change(screen.getByLabelText('Variant'), { target: { value: 'f2' } });
    }
    expect(await screen.findByRole('button', { name: 'Load 3D view' })).toBeInTheDocument();
    expect(screen.queryByLabelText('3D viewer')).not.toBeInTheDocument();
  });

  test(`${scope}: a mesh at both automatic limits loads immediately`, async () => {
    setup(scope, 250_000, 50 * 1024 * 1024);
    expect(await screen.findByLabelText('3D viewer')).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Load 3D view' })).not.toBeInTheDocument());
  });
}

function setup(scope: string, triangleCount: number | null, sizeBytes: number) {
  window.history.pushState({}, '', scope === 'owner' ? '/models/m1' : '/s/token');
  const file = { id: 'f1', modelId: 'm1', filename: 'first.stl', relPath: 'files/first.stl', format: 'stl', sizeBytes, triangleCount, bboxX: 1, bboxY: 1, bboxZ: 1 };
  const model = { id: 'm1', title: 'Model', description: '', totalBytes: sizeBytes, files: [file, { ...file, id: 'f2', filename: 'second.stl' }] };
  vi.stubGlobal('EventSource', class { addEventListener = vi.fn(); close = vi.fn(); });
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url === '/api/me') return Response.json({ authenticated: true });
    if (url === '/api/models/m1') return Response.json(model);
    if (url === '/api/public/token') return Response.json({ model, share: {} });
    if (url.endsWith('/status')) return new Response(null, { status: 204 });
    return Response.json([]);
  }));
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })}><App /></QueryClientProvider>);
}
