import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen } from '@testing-library/react';
import '@testing-library/jest-dom/vitest';
import { afterEach, expect, test, vi } from 'vitest';
import { App } from './App';

afterEach(() => vi.unstubAllGlobals());

test.each(['/', '/models/m1', '/collections/fixtures', '/s/model-token', '/s/collection-token'])('empty model remains usable at %s', async (path) => {
  const model = { id: 'm1', title: 'Empty model', description: 'Saved metadata', totalBytes: 0, files: [] };
  const collection = { id: 'c1', name: 'Fixtures', slug: 'fixtures', description: '', modelCount: 1, modelIds: ['m1'], models: [model] };
  window.history.pushState({}, '', path);
  vi.stubGlobal('EventSource', class { addEventListener = vi.fn(); close = vi.fn(); });
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url === '/api/me') return Response.json({ authenticated: true });
    if (url === '/api/models/m1') return Response.json(model);
    if (url.startsWith('/api/models?')) return Response.json({ items: [model], nextCursor: '' });
    if (url === '/api/collections/fixtures') return Response.json(collection);
    if (url.startsWith('/api/collections?') || url === '/api/collections') return Response.json([collection]);
    if (url.endsWith('/status')) return new Response(null, { status: 204 });
    if (url === '/api/public/model-token') return Response.json({ model, share: {} });
    if (url === '/api/public/collection-token') return Response.json({ model, collection, share: {} });
    return Response.json([]);
  }));
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><App /></QueryClientProvider>);
  if (path === '/models/m1') {
    expect(await screen.findByDisplayValue('Empty model')).toBeInTheDocument();
    expect(screen.getByText('No model files. Add a variant to preview or download.')).toBeInTheDocument();
    expect(screen.getByText('Add variants')).toBeInTheDocument();
  } else {
    expect((await screen.findAllByText('Empty model')).length).toBeGreaterThan(0);
    if (path.startsWith('/s/')) expect(screen.getByText('No model files are available.')).toBeInTheDocument();
  }
  expect(screen.queryByRole('button', { name: 'Load 3D view' })).not.toBeInTheDocument();
});
