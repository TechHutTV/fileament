import { focusManager, QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import '@testing-library/jest-dom/vitest';
import { afterEach, expect, test, vi } from 'vitest';
import { App } from './App';

vi.mock('./Viewer', () => ({ default: ({ file }: { file: { id: string } }) => <div aria-label={`3D view ${file.id}`} /> }));
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); focusManager.setFocused(undefined); });
function renderPage(path: string) {
  window.history.replaceState({}, '', path);
  vi.stubGlobal('EventSource', class { addEventListener = vi.fn(); close = vi.fn(); });
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })}><App /></QueryClientProvider>);
}
async function refresh() { await act(async () => { focusManager.setFocused(false); focusManager.setFocused(true); }); }

test('catalog thumbnails load without IntersectionObserver and update with the selected cover', async () => {
  vi.stubGlobal('IntersectionObserver', undefined);
  let thumb = 'first.png';
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    if (String(input) === '/api/me') return Response.json({ authenticated: true });
    if (String(input).startsWith('/api/models?')) return Response.json({ items: [{ id: 'm1', title: 'Part', primaryThumb: thumb, totalBytes: 1, files: [] }], nextCursor: '' });
    return Response.json([]);
  }));
  renderPage('/');
  expect(await screen.findByAltText('Part thumbnail')).toHaveAttribute('src', '/thumbs/m1/first.png');
  expect(screen.getByAltText('Part thumbnail')).toHaveAttribute('loading', 'lazy');
  thumb = 'chosen.png'; await refresh();
  await waitFor(() => expect(screen.getByAltText('Part thumbnail')).toHaveAttribute('src', '/thumbs/m1/chosen.png'));
  expect(screen.getByRole('link', { name: /Part/ })).toHaveAttribute('href', '/models/m1');
});

const largeFile = { id: 'f1', modelId: 'm1', filename: 'first.stl', relPath: 'files/first.stl', format: 'stl', sizeBytes: 60 * 1024 * 1024, triangleCount: 1, bboxX: 1, bboxY: 1, bboxZ: 1 };
const replacementFile = { ...largeFile, id: 'f2', filename: 'replacement.stl', relPath: 'files/replacement.stl' };
const model = { id: 'm1', title: 'Part', description: '', totalBytes: largeFile.sizeBytes * 2, files: [largeFile, replacementFile], images: [] };

test.each(['delete', 'refresh'])('a %s removing the approved variant does not load another large file automatically', async (method) => {
  let removed = false;
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    if (String(input) === '/api/me') return Response.json({ authenticated: true });
    if (String(input) === '/api/models/m1/files/f1' && init?.method === 'DELETE') { removed = true; return new Response(null, { status: 204 }); }
    if (String(input) === '/api/models/m1') return Response.json(removed ? { ...model, files: [replacementFile] } : model);
    return Response.json([]);
  }));
  renderPage('/models/m1');
  fireEvent.click(await screen.findByRole('button', { name: 'Load 3D view' }));
  expect(await screen.findByLabelText('3D view f1')).toBeInTheDocument();
  fireEvent.change(screen.getByLabelText('Title'), { target: { value: 'Unsaved name' } });
  if (method === 'delete') {
    fireEvent.click(screen.getByRole('button', { name: 'Delete first.stl' }));
    fireEvent.click(screen.getByRole('button', { name: 'Delete variant' }));
  } else { removed = true; await refresh(); }
  expect(await screen.findByRole('button', { name: 'Load 3D view' })).toBeInTheDocument();
  expect(screen.queryByLabelText('3D view f1')).not.toBeInTheDocument();
  expect(screen.queryByLabelText('3D view f2')).not.toBeInTheDocument();
  expect(screen.getByRole('link', { name: /Download replacement.stl/ })).toHaveAttribute('href', '/files/m1/f2');
  expect(screen.getByLabelText('Title')).toHaveValue('Unsaved name');
  fireEvent.click(screen.getByRole('button', { name: 'Load 3D view' }));
  expect(await screen.findByLabelText('3D view f2')).toBeInTheDocument();
});

test('refreshing metadata or removing another file preserves the approved viewer and draft', async () => {
  let changed = false;
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    if (String(input) === '/api/me') return Response.json({ authenticated: true });
    if (String(input) === '/api/models/m1') return Response.json(changed ? { ...model, description: 'New notes', files: [largeFile] } : model);
    return Response.json([]);
  }));
  renderPage('/models/m1');
  fireEvent.click(await screen.findByRole('button', { name: 'Load 3D view' }));
  await screen.findByLabelText('3D view f1');
  fireEvent.change(screen.getByLabelText('Title'), { target: { value: 'My draft' } });
  changed = true; await refresh();
  await waitFor(() => expect(screen.getByLabelText('Notes')).toHaveValue('New notes'));
  expect(screen.getByLabelText('3D view f1')).toBeInTheDocument();
  expect(screen.queryByRole('button', { name: 'Load 3D view' })).not.toBeInTheDocument();
  expect(screen.getByLabelText('Title')).toHaveValue('My draft');
});
