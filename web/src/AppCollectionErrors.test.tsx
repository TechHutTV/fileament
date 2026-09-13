import { focusManager, QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import '@testing-library/jest-dom/vitest';
import { afterEach, expect, test, vi } from 'vitest';
import { App } from './App';

afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); focusManager.setFocused(undefined); });
const collection = { id: 'c1', slug: 'parts', name: 'Parts', description: 'Notes', modelCount: 0, models: [] };
function renderPage() {
  window.history.replaceState({}, '', '/collections/parts');
  vi.stubGlobal('EventSource', class { addEventListener = vi.fn(); close = vi.fn(); });
  return render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><App /></QueryClientProvider>);
}
async function refresh() { await act(async () => { focusManager.setFocused(false); focusManager.setFocused(true); }); }

test.each([
  [404, 'Collection not found'],
  [403, 'You do not have access to this collection'],
  [503, 'Fileament is temporarily unavailable or recovering data'],
  [500, 'Collection could not be loaded'],
  [0, 'Unable to reach Fileament'],
])('explains collection load failure %i and supports retry', async (code, message) => {
  let failed = true;
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    if (String(input) === '/api/me') return Response.json({ authenticated: true });
    if (String(input) === '/api/collections/parts') {
      if (failed && !code) throw new TypeError('network unavailable');
      return Response.json(failed ? { error: 'internal details' } : collection, { status: failed ? code : 200 });
    }
    return Response.json([]);
  }));
  renderPage();
  expect(await screen.findByRole('alert')).toHaveTextContent(message);
  expect(screen.queryByText('internal details')).not.toBeInTheDocument();
  expect(screen.queryByLabelText('Collection name')).not.toBeInTheDocument();
  failed = false;
  fireEvent.click(screen.getByRole('button', { name: 'Retry collection' }));
  expect(await screen.findByLabelText('Collection name')).toHaveValue('Parts');
});

test('a refresh confirming deletion removes cached editing and confirmation controls', async () => {
  let missing = false;
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    if (String(input) === '/api/me') return Response.json({ authenticated: true });
    if (String(input) === '/api/collections/parts') return Response.json(missing ? { error: 'missing' } : collection, { status: missing ? 404 : 200 });
    return Response.json([]);
  }));
  renderPage();
  await screen.findByLabelText('Collection name');
  fireEvent.click(screen.getByRole('button', { name: 'Delete collection' }));
  expect(screen.getByRole('alertdialog')).toBeInTheDocument();
  missing = true; await refresh();
  expect(await screen.findByRole('alert')).toHaveTextContent('Collection not found');
  expect(screen.queryByLabelText('Collection name')).not.toBeInTheDocument();
  expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument();
  expect(screen.queryByRole('button', { name: 'Delete collection' })).not.toBeInTheDocument();
  missing = false; fireEvent.click(screen.getByRole('button', { name: 'Retry collection' }));
  await screen.findByLabelText('Collection name');
  expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument();
});

test('a temporary refresh failure retains the draft through retry', async () => {
  let unavailable = false;
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    if (String(input) === '/api/me') return Response.json({ authenticated: true });
    if (String(input) === '/api/collections/parts') return Response.json(unavailable ? { error: 'recovering' } : collection, { status: unavailable ? 503 : 200 });
    return Response.json([]);
  }));
  renderPage();
  fireEvent.change(await screen.findByLabelText('Collection name'), { target: { value: 'Unsaved name' } });
  unavailable = true; await refresh();
  expect(await screen.findByRole('alert')).toHaveTextContent('temporarily unavailable');
  expect(screen.getByLabelText('Collection name')).toHaveValue('Unsaved name');
  unavailable = false; fireEvent.click(screen.getByRole('button', { name: 'Retry collection' }));
  await waitFor(() => expect(screen.queryByRole('alert')).not.toBeInTheDocument());
  expect(screen.getByLabelText('Collection name')).toHaveValue('Unsaved name');
});

test('collection authentication failure returns to login and clears cached data', async () => {
  let authenticated = true;
  let rejectCollection = false;
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    if (String(input) === '/api/me') return Response.json({ authenticated });
    if (String(input) === '/api/collections/parts' && rejectCollection) authenticated = false;
    if (String(input) === '/api/collections/parts') return Response.json(authenticated ? collection : { error: 'unauthorized' }, { status: authenticated ? 200 : 401 });
    return Response.json([]);
  }));
  renderPage();
  await screen.findByLabelText('Collection name');
  rejectCollection = true; await refresh();
  expect(await screen.findByText('Owner login')).toBeInTheDocument();
  expect(screen.queryByLabelText('Collection name')).not.toBeInTheDocument();
});

test('leaving a collection cancels its in-flight read', async () => {
  let signal: AbortSignal | null | undefined;
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    if (String(input) === '/api/me') return Response.json({ authenticated: true });
    if (String(input) === '/api/collections/parts') { signal = init?.signal; return new Promise<Response>(() => {}); }
    if (String(input).startsWith('/api/models?')) return Response.json({ items: [], nextCursor: '' });
    return Response.json([]);
  }));
  renderPage();
  await screen.findByText('Loading collection');
  act(() => { window.history.pushState({}, '', '/'); window.dispatchEvent(new Event('fileament:navigate')); });
  await waitFor(() => expect(signal?.aborted).toBe(true));
});

test('retrying a failed member page loads that page and preserves the collection draft', async () => {
  let fail = true;
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url === '/api/me') return Response.json({ authenticated: true });
    if (url === '/api/collections/parts') return Response.json({ ...collection, nextCursor: 'next' });
    if (url === '/api/collections/parts?cursor=next') return Response.json(fail ? { error: 'temporary' } : { ...collection, nextCursor: '', models: [{ id: 'm1', title: 'Later member', totalBytes: 1, files: [] }] }, { status: fail ? 503 : 200 });
    return Response.json([]);
  }));
  renderPage();
  fireEvent.change(await screen.findByLabelText('Collection name'), { target: { value: 'My draft' } });
  fireEvent.click(screen.getByRole('button', { name: 'Load more models' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('temporarily unavailable');
  fail = false; fireEvent.click(screen.getByRole('button', { name: 'Retry collection' }));
  expect(await screen.findByRole('link', { name: /Later member/ })).toBeInTheDocument();
  expect(screen.getByLabelText('Collection name')).toHaveValue('My draft');
});
