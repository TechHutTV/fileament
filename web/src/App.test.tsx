import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import '@testing-library/jest-dom/vitest';
import { afterEach, beforeEach, expect, test, vi } from 'vitest';
import { App } from './App';

const model = {
	id: 'm1',
	title: 'Calibration Cube',
	description: '[Documentation](https://example.com)',
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

test('accepts an empty setup success response and transitions to login', async () => {
  const calls: string[] = [];
  let setupComplete = false;
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push(`${init?.method ?? 'GET'} ${String(input)}`);
    if (String(input).includes('/api/me')) return Response.json({ authenticated: false, setupRequired: !setupComplete });
    if (String(input).includes('/api/auth/setup')) {
      setupComplete = true;
      return new Response(null, { status: 201 });
    }
    return Response.json({});
  }));
  renderApp();
  expect(await screen.findByText('Set owner password')).toBeInTheDocument();
  fireEvent.change(screen.getByLabelText('Password'), { target: { value: 'correct horse password' } });
  fireEvent.click(screen.getByRole('button', { name: /create owner/i }));
  await waitFor(() => expect(calls).toContain('POST /api/auth/setup'));
  expect(await screen.findByText('Owner login')).toBeInTheDocument();
  expect(screen.getByText('Your 3D files, organized.')).toBeInTheDocument();
  fireEvent.click(screen.getByRole('button', { name: /show password/i }));
  expect(screen.getByLabelText('Password')).toHaveAttribute('type', 'text');
  expect(screen.queryByText('Authentication failed')).not.toBeInTheDocument();
});

test('manages a dropped multi-model upload queue', async () => {
  window.history.pushState({}, '', '/upload');
  const uploads: string[] = [];
  const calls: string[] = [];
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push(`${init?.method ?? 'GET'} ${url}`);
    if (url.includes('/api/me')) return Response.json({ authenticated: true, setupRequired: false });
    if (url === '/api/models' && init?.method === 'POST') {
      const file = (init.body as FormData).get('file') as File;
      uploads.push(file.name);
      return Response.json({
        ...model,
        id: file.name,
        title: file.name,
        totalBytes: file.size,
        primaryThumb: 'card.png',
        files: [{ ...model.files[0], id: `${file.name}-file`, modelId: file.name, filename: file.name, sizeBytes: file.size }],
      }, { status: 201 });
    }
    if (url.startsWith('/api/models/') && init?.method === 'DELETE') return new Response(null, { status: 204 });
    if (url.includes('/api/models?')) return Response.json({ items: [], nextCursor: '' });
    if (url.includes('/api/collections') || url.includes('/api/tags')) return Response.json([]);
    return Response.json({});
  }));
  renderApp();

  const dropzone = await screen.findByRole('button', { name: /drop 3d files/i });
  expect(screen.getByLabelText(/choose 3d files/i)).toHaveAttribute('multiple');
  const cube = new File(['solid cube'], 'cube.stl', { type: 'model/stl' });
  const bracket = new File(['solid bracket'], 'bracket.stl', { type: 'model/stl' });
  fireEvent.drop(dropzone, { dataTransfer: { files: [cube, bracket] } });

  await waitFor(() => expect(uploads).toEqual(['cube.stl', 'bracket.stl']));
  expect(await screen.findByAltText('cube.stl thumbnail')).toHaveAttribute('src', '/thumbs/cube.stl/card.png');
  expect(screen.getByAltText('bracket.stl thumbnail')).toHaveAttribute('src', '/thumbs/bracket.stl/card.png');

  fireEvent.click(screen.getByRole('button', { name: 'Remove cube.stl' }));
  await waitFor(() => expect(calls).toContain('DELETE /api/models/cube.stl'));
  expect(screen.queryByText('cube.stl')).not.toBeInTheDocument();

  fireEvent.click(screen.getByRole('button', { name: /finish and view library/i }));
  expect(window.location.pathname).toBe('/');
});

test('serializes automatic uploads to avoid concurrent database writes', async () => {
  window.history.pushState({}, '', '/upload');
  const uploads: string[] = [];
  let releaseFirst: (response: Response) => void = () => undefined;
  const firstResponse = new Promise<Response>((resolve) => { releaseFirst = resolve; });
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url.includes('/api/me')) return Response.json({ authenticated: true, setupRequired: false });
    if (url === '/api/models' && init?.method === 'POST') {
      const file = (init.body as FormData).get('file') as File;
      uploads.push(file.name);
      if (uploads.length === 1) return firstResponse;
      return Response.json({ ...model, id: file.name, title: file.name }, { status: 201 });
    }
    return Response.json({});
  }));
  renderApp();

  const dropzone = await screen.findByRole('button', { name: /drop 3d files/i });
  fireEvent.drop(dropzone, { dataTransfer: { files: [new File(['a'], 'first.stl'), new File(['b'], 'second.stl')] } });

  await waitFor(() => expect(uploads).toEqual(['first.stl']));
  releaseFirst(Response.json({ ...model, id: 'first.stl', title: 'first.stl' }, { status: 201 }));
  await waitFor(() => expect(uploads).toEqual(['first.stl', 'second.stl']));
});

test('keeps a cancelled upload visible when server cleanup fails', async () => {
  window.history.pushState({}, '', '/upload');
  let releaseUpload: (response: Response) => void = () => undefined;
  const uploadResponse = new Promise<Response>((resolve) => { releaseUpload = resolve; });
  const calls: string[] = [];
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push(`${init?.method ?? 'GET'} ${url}`);
    if (url.includes('/api/me')) return Response.json({ authenticated: true, setupRequired: false });
    if (url === '/api/models' && init?.method === 'POST') return uploadResponse;
    if (url === '/api/models/delayed.stl' && init?.method === 'DELETE') return new Response('cleanup failed', { status: 500 });
    return Response.json({});
  }));
  renderApp();

  const dropzone = await screen.findByRole('button', { name: /drop 3d files/i });
  fireEvent.drop(dropzone, { dataTransfer: { files: [new File(['solid delayed'], 'delayed.stl')] } });
  fireEvent.click(await screen.findByRole('button', { name: 'Cancel delayed.stl upload' }));
  releaseUpload(Response.json({ ...model, id: 'delayed.stl', title: 'delayed.stl' }, { status: 201 }));

  await waitFor(() => expect(calls).toContain('DELETE /api/models/delayed.stl'));
  expect(await screen.findByText('Upload completed, but could not remove the model.')).toBeInTheDocument();
  expect(screen.getByText('delayed.stl')).toBeInTheDocument();
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
  expect(screen.getByRole('heading', { name: 'Models' })).toBeInTheDocument();
  expect(screen.getByText('1 model shown')).toBeInTheDocument();
  expect(screen.getByText('STL')).toBeInTheDocument();
  expect(screen.getByText(/12 tris/i)).toBeInTheDocument();
  expect(screen.getByRole('link', { name: /upload/i })).toBeInTheDocument();
  fireEvent.change(screen.getByLabelText('Search models'), { target: { value: 'cube' } });
  fireEvent.change(screen.getByLabelText('Tag'), { target: { value: 'tools' } });
  fireEvent.change(screen.getByLabelText('Collection'), { target: { value: 'fixtures' } });
  fireEvent.change(screen.getByLabelText('Sort'), { target: { value: 'title' } });
  await waitFor(() => expect(urls.some((u) => u.includes('q=cube') && u.includes('tag=tools') && u.includes('collection=fixtures') && u.includes('sort=title'))).toBe(true));
  fireEvent.click(screen.getByRole('button', { name: /load more/i }));
  await waitFor(() => expect(urls.some((u) => u.includes('cursor=next-page'))).toBe(true));
});

test('polishes empty collections and settings with active navigation and grouped states', async () => {
  window.history.pushState({}, '', '/collections');
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url.includes('/api/me')) return Response.json({ authenticated: true, setupRequired: false });
    if (url.includes('/api/collections') && init?.method === 'POST') return Response.json({ id: 'c1', name: 'Fixtures', slug: 'fixtures', description: '' }, { status: 201 });
    if (url.includes('/api/collections')) return Response.json([]);
    if (url.includes('/api/storage')) return Response.json({ totalBytes: 2048 });
    if (url.includes('/api/shares')) return Response.json([]);
    return Response.json({});
  }));
  renderApp();

  expect(await screen.findByRole('heading', { name: 'Collections' })).toBeInTheDocument();
  expect(await screen.findByText('No collections yet')).toBeInTheDocument();
  expect(screen.getByRole('link', { name: /collections/i })).toHaveAttribute('aria-current', 'page');
  fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'Fixtures' } });
  fireEvent.change(screen.getByLabelText('Description'), { target: { value: 'Useful parts' } });
  fireEvent.click(screen.getByRole('button', { name: /create collection/i }));
  await waitFor(() => expect(screen.getByLabelText('Name')).toHaveValue(''));

  window.history.pushState({}, '', '/settings');
  fireEvent(window, new Event('fileament:navigate'));
  expect(await screen.findByRole('heading', { name: 'Security' })).toBeInTheDocument();
  expect(await screen.findByText('2.0 KB')).toBeInTheDocument();
  expect(await screen.findByText('No share links yet')).toBeInTheDocument();
  expect(screen.getByRole('link', { name: /settings/i })).toHaveAttribute('aria-current', 'page');
});

test('detail management actions call owner APIs and preserve viewer gate', async () => {
  window.history.pushState({}, '', '/models/m1');
  const calls: string[] = [];
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push(`${init?.method ?? 'GET'} ${url}`);
    if (url.includes('/api/me')) return Response.json({ authenticated: true, setupRequired: false });
    if (url.includes('/api/models/m1')) return Response.json(model);
    if (url.includes('/api/collections')) return Response.json([{ id: 'c1', name: 'Fixtures', slug: 'fixtures', description: '', modelIds: ['m1'] }]);
    if (url.includes('/api/shares')) return Response.json([]);
    if (init?.method === 'PATCH' || init?.method === 'POST' || init?.method === 'DELETE' || init?.method === 'PUT') return Response.json(model);
    return Response.json({});
  }));
  renderApp();
  expect(await screen.findByDisplayValue('Calibration Cube')).toBeInTheDocument();
  expect(screen.getByRole('link', { name: 'Documentation' })).toHaveAttribute('href', 'https://example.com');
  expect(screen.getByRole('button', { name: /load 3d view/i })).toBeInTheDocument();
  const membership = screen.getByRole('checkbox', { name: 'Fixtures' });
  expect(membership).toBeChecked();
  fireEvent.click(membership);
  await waitFor(() => expect(calls).toContain('DELETE /api/collections/c1/models/m1'));
  fireEvent.change(screen.getByLabelText('Title'), { target: { value: 'Updated Cube' } });
  fireEvent.click(screen.getByRole('button', { name: /save changes/i }));
  await waitFor(() => expect(calls).toContain('PATCH /api/models/m1'));
  fireEvent.click(screen.getByRole('button', { name: /create share/i }));
  await waitFor(() => expect(calls).toContain('POST /api/shares'));
});

test('collection detail supports metadata, cover, ordering, and deletion', async () => {
  window.history.pushState({}, '', '/collections/fixtures');
  const second = { ...model, id: 'm2', title: 'Second', files: [{ ...model.files[0], id: 'f2', modelId: 'm2' }] };
  const calls: string[] = [];
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push(`${init?.method ?? 'GET'} ${url}`);
    if (url.includes('/api/me')) return Response.json({ authenticated: true, setupRequired: false });
    if (url.includes('/api/collections/fixtures')) return Response.json({ id: 'c1', name: 'Fixtures', slug: 'fixtures', description: 'Useful parts', coverModelId: 'm1', modelIds: ['m1', 'm2'], models: [model, second] });
    if (init?.method === 'PATCH') return Response.json({ id: 'c1', name: 'Updated Fixtures', slug: 'fixtures', description: '', coverModelId: 'm2', modelIds: ['m1', 'm2'], models: [model, second] });
    return new Response(null, { status: 204 });
  }));
  renderApp();
  expect(await screen.findByDisplayValue('Fixtures')).toBeInTheDocument();
  fireEvent.change(screen.getByLabelText('Collection name'), { target: { value: 'Updated Fixtures' } });
  fireEvent.change(screen.getByLabelText('Cover model'), { target: { value: 'm2' } });
  fireEvent.click(screen.getByRole('button', { name: /save collection/i }));
  await waitFor(() => expect(calls).toContain('PATCH /api/collections/c1'));
  fireEvent.click(screen.getByRole('button', { name: /move calibration cube down/i }));
  await waitFor(() => expect(calls).toContain('PUT /api/collections/c1/order'));
  fireEvent.click(screen.getByRole('button', { name: /delete collection/i }));
  await waitFor(() => expect(calls).toContain('DELETE /api/collections/c1'));
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
