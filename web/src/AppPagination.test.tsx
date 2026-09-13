import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import '@testing-library/jest-dom/vitest';
import { afterEach, expect, test, vi } from 'vitest';
import { App } from './App';

afterEach(() => vi.unstubAllGlobals());

const card = { id: 'm1', title: 'First part', totalBytes: 120, files: [{ format: 'stl', triangleCount: 12 }] };
const nextCard = { ...card, id: 'm2', title: 'Later part' };
const collection = { id: 'c1', name: 'Parts', slug: 'parts', description: '', modelCount: 2, models: [card], nextCursor: 'page-two' };
const selected = { ...nextCard, description: 'Full selected details', files: [], images: [] };

function renderPage(path: string) {
  window.history.pushState({}, '', path);
  vi.stubGlobal('EventSource', class { addEventListener = vi.fn(); close = vi.fn(); });
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><App /></QueryClientProvider>);
}

test('catalog cards render a summary without full variant or description fields', async () => {
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url === '/api/me') return Response.json({ authenticated: true });
    if (url.startsWith('/api/models?')) return Response.json({ items: [card], nextCursor: '' });
    return Response.json([]);
  }));
  renderPage('/');
  expect(await screen.findByRole('link', { name: /First part/ })).toHaveAttribute('href', '/models/m1');
  expect(screen.getByText('STL')).toBeInTheDocument();
  expect(screen.getByText('12 tris')).toBeInTheDocument();
});

test('owner collection loads another page and moves a member beyond the loaded page', async () => {
  const moves: unknown[] = [];
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url === '/api/me') return Response.json({ authenticated: true });
    if (url.endsWith('/order')) { moves.push(JSON.parse(String(init?.body))); return new Response(null, { status: 204 }); }
    if (url === '/api/collections/parts') return Response.json(collection);
    if (url === '/api/collections/parts?cursor=page-two') return Response.json({ ...collection, models: [nextCard], nextCursor: '' });
    return Response.json([]);
  }));
  renderPage('/collections/parts');
  fireEvent.click(await screen.findByRole('button', { name: 'Move First part down' }));
  await waitFor(() => expect(moves).toEqual([{ modelId: 'm1', direction: 'down' }]));
  fireEvent.click(screen.getByRole('button', { name: 'Load more models' }));
  expect(await screen.findByRole('link', { name: /Later part/ })).toBeInTheDocument();
  expect(screen.getByText('2 of 2 models shown')).toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Move Later part down' })).toBeDisabled();
  expect(screen.queryByRole('button', { name: 'Load more models' })).not.toBeInTheDocument();
});

test('public selection loads full details independently of the paged member list', async () => {
  const calls: string[] = [];
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    calls.push(url);
    if (url.endsWith('/status')) return new Response(null, { status: 204 });
    if (url === '/api/public/token?model=m2') return Response.json({ model: selected, collection });
    if (url === '/api/public/token?model=m2&cursor=page-two&refresh=1') return Response.json({ model: selected, collection: { ...collection, models: [nextCard], nextCursor: '' } });
    return Response.json({});
  }));
  renderPage('/s/token?model=m2');
  expect(await screen.findByRole('heading', { name: 'Later part' })).toBeInTheDocument();
  expect(screen.getByText('Full selected details')).toBeInTheDocument();
  fireEvent.click(screen.getByRole('button', { name: 'Load more models' }));
  expect(await screen.findByRole('link', { name: 'Later part' })).toHaveAttribute('href', '/s/token?model=m2');
  expect(calls).toContain('/api/public/token?model=m2&cursor=page-two&refresh=1');
  expect(screen.queryByRole('button', { name: 'Load more models' })).not.toBeInTheDocument();
});
