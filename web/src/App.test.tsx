import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import '@testing-library/jest-dom/vitest';
import { afterEach, beforeEach, expect, test, vi } from 'vitest';
import { App } from './App';

const model = {
  id: 'm1',
  title: 'Calibration Cube',
  description: 'Notes',
  totalBytes: 1024,
  files: [{ id: 'f1', modelId: 'm1', filename: 'cube.stl', relPath: 'files/cube.stl', format: 'stl', sizeBytes: 60 * 1024 * 1024, triangleCount: 12, bboxX: 1, bboxY: 1, bboxZ: 1 }],
  images: [],
  tags: ['tools'],
};

beforeEach(() => {
  window.history.pushState({}, '', '/');
  vi.stubGlobal('EventSource', class {
    addEventListener = vi.fn();
    close = vi.fn();
  });
});

afterEach(() => {
  vi.restoreAllMocks();
});

test('renders setup and login flows', async () => {
  const calls: string[] = [];
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push(`${init?.method ?? 'GET'} ${String(input)}`);
    if (String(input).includes('/api/me')) return Response.json({ authenticated: false, setupRequired: true });
    return new Response(null, { status: 201 });
  }));
  renderApp();
  expect(await screen.findByText('Set owner password')).toBeInTheDocument();
  fireEvent.change(screen.getByLabelText('Password'), { target: { value: 'correct horse password' } });
  fireEvent.click(screen.getByRole('button', { name: /create owner/i }));
  await waitFor(() => expect(calls).toContain('POST /api/auth/setup'));
});

test('catalog exposes filters, sorting, pagination, and owner nav', async () => {
  const urls: string[] = [];
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    urls.push(url);
    if (url.includes('/api/me')) return Response.json({ authenticated: true, setupRequired: false });
    if (url.includes('/api/tags')) return Response.json([{ name: 'Tools', slug: 'tools' }]);
    if (url.includes('/api/collections')) return Response.json([{ id: 'c1', name: 'Fixtures', slug: 'fixtures', description: '' }]);
    if (url.includes('/api/models')) return Response.json({ items: [model], nextCursor: url.includes('cursor=') ? '' : 'next-page' });
    return Response.json({});
  }));
  renderApp();
  expect(await screen.findByText('Calibration Cube')).toBeInTheDocument();
  expect(screen.getByRole('link', { name: /upload/i })).toBeInTheDocument();
  fireEvent.change(screen.getByLabelText('Search models'), { target: { value: 'cube' } });
  fireEvent.change(screen.getByLabelText('Tag'), { target: { value: 'tools' } });
  fireEvent.change(screen.getByLabelText('Collection'), { target: { value: 'fixtures' } });
  fireEvent.change(screen.getByLabelText('Sort'), { target: { value: 'title' } });
  await waitFor(() => expect(urls.some((u) => u.includes('q=cube') && u.includes('tag=tools') && u.includes('collection=fixtures') && u.includes('sort=title'))).toBe(true));
  fireEvent.click(screen.getByRole('button', { name: /load more/i }));
  await waitFor(() => expect(urls.some((u) => u.includes('cursor=next-page'))).toBe(true));
});

test('detail management actions call owner APIs and preserve viewer gate', async () => {
  window.history.pushState({}, '', '/models/m1');
  const calls: string[] = [];
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push(`${init?.method ?? 'GET'} ${url}`);
    if (url.includes('/api/me')) return Response.json({ authenticated: true, setupRequired: false });
    if (url.includes('/api/models/m1')) return Response.json(model);
    if (url.includes('/api/collections')) return Response.json([]);
    if (url.includes('/api/shares')) return Response.json([]);
    if (init?.method === 'PATCH' || init?.method === 'POST' || init?.method === 'DELETE' || init?.method === 'PUT') return Response.json(model);
    return Response.json({});
  }));
  renderApp();
  expect(await screen.findByDisplayValue('Calibration Cube')).toBeInTheDocument();
  expect(screen.getByRole('button', { name: /load 3d view/i })).toBeInTheDocument();
  fireEvent.change(screen.getByLabelText('Title'), { target: { value: 'Updated Cube' } });
  fireEvent.click(screen.getByRole('button', { name: /save changes/i }));
  await waitFor(() => expect(calls).toContain('PATCH /api/models/m1'));
  fireEvent.click(screen.getByRole('button', { name: /create share/i }));
  await waitFor(() => expect(calls).toContain('POST /api/shares'));
});

test('public collection cards select models within the same share and use token asset URLs', async () => {
  window.history.pushState({}, '', '/s/sharetoken?model=m1');
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    if (String(input).includes('/api/public/sharetoken')) return Response.json({ collection: { id: 'c1', name: 'Shared', slug: 'shared', description: '', models: [{ ...model, primaryThumb: 'card.jpg' }] } });
    return Response.json({});
  }));
  renderApp();
  expect(await screen.findByText('Shared')).toBeInTheDocument();
  expect(screen.getByRole('link', { name: 'Calibration Cube' })).toHaveAttribute('href', '/s/sharetoken?model=m1');
  expect(screen.getByRole('button', { name: /load 3d view/i })).toBeInTheDocument();
  expect(screen.getByRole('link', { name: /cube.stl/i })).toHaveAttribute('href', '/api/public/sharetoken/files/f1');
});

function renderApp() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  render(<QueryClientProvider client={client}><App /></QueryClientProvider>);
}
