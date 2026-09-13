import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, fireEvent, render, screen } from '@testing-library/react';
import '@testing-library/jest-dom/vitest';
import { afterEach, expect, test, vi } from 'vitest';
import { App } from './App';

vi.mock('./Viewer', () => ({ default: () => <div aria-label="Shared 3D view" /> }));
afterEach(() => { vi.useRealTimers(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });
const file = { id: 'f1', modelId: 'm1', filename: 'part.stl', relPath: 'files/part.stl', format: 'stl', sizeBytes: 60 * 1024 * 1024, triangleCount: 1, bboxX: 1, bboxY: 1, bboxZ: 1 };
const model = { id: 'm1', title: 'Original part', description: 'Original details', totalBytes: file.sizeBytes, files: [file], images: [] };
const other = { ...model, id: 'm2', title: 'Other part', files: [] };
const collection = { id: 'c1', name: 'Shared parts', slug: 'parts', description: '', modelCount: 2, models: [model], nextCursor: 'old-next' };
function renderPage(path = '/s/token?model=m1') {
  window.history.replaceState({}, '', path);
  return render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><App /></QueryClientProvider>);
}
async function tick(ms = 60_000) { await act(async () => { await vi.advanceTimersByTimeAsync(ms); }); }

test('refreshes edited metadata and loaded collection pages with new cursors, without counting views', async () => {
  const calls: string[] = [];
  let changed = false;
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input); calls.push(url);
    if (url.endsWith('/status')) return new Response(null, { status: 204 });
    const query = new URL(url, 'http://localhost').searchParams;
    const current = changed ? { ...model, title: 'Edited part', description: 'Updated details' } : model;
    return Response.json({ model: current, collection: { ...collection, models: query.has('cursor') ? [other] : [current], nextCursor: query.has('cursor') ? '' : changed ? 'new-next' : 'old-next' } });
  }));
  vi.useFakeTimers(); renderPage(); await tick(20);
  expect(screen.getByRole('heading', { name: 'Original part' })).toBeInTheDocument();
  fireEvent.click(screen.getByRole('button', { name: 'Load more models' }));
  await tick(20);
  changed = true;
  await tick();
  expect(screen.getByRole('heading', { name: 'Edited part' })).toBeInTheDocument();
  expect(screen.getByText('Updated details')).toBeInTheDocument();
  expect(screen.getByRole('link', { name: 'Other part' })).toBeInTheDocument();
  const metadata = calls.filter((url) => !url.endsWith('/status'));
  expect(metadata).toHaveLength(4);
  expect(metadata.slice(1).every((url) => new URL(url, 'http://localhost').searchParams.get('refresh') === '1')).toBe(true);
  expect(new URL(metadata[3], 'http://localhost').searchParams.get('cursor')).toBe('new-next');
});

test('removing the selected model clears its viewer and downloads even if a later refreshed page fails', async () => {
  let removed = false;
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url.endsWith('/status')) return new Response(null, { status: 204 });
    const query = new URL(url, 'http://localhost').searchParams;
    if (removed && query.has('cursor')) return Response.json({ error: 'maintenance' }, { status: 503 });
    return Response.json({ model: removed ? null : model, modelUnavailable: removed, collection: { ...collection, models: query.has('cursor') || removed ? [other] : [model], nextCursor: query.has('cursor') ? '' : 'next' } });
  }));
  vi.useFakeTimers(); renderPage(); await tick(20);
  fireEvent.click(screen.getByRole('button', { name: 'Load 3D view' }));
  await tick(20);
  fireEvent.click(screen.getByRole('button', { name: 'Load more models' }));
  await tick(20);
  removed = true; await tick();
  expect(screen.queryByLabelText('Shared 3D view')).not.toBeInTheDocument();
  expect(screen.queryByRole('link', { name: /part.stl/ })).not.toBeInTheDocument();
  expect(screen.queryByRole('link', { name: 'Original part' })).not.toBeInTheDocument();
  expect(screen.getByText('This model is no longer in the shared collection. Choose another model.')).toBeInTheDocument();
  expect(screen.getByRole('link', { name: 'Other part' })).toBeInTheDocument();
  expect(screen.getByRole('alert')).toHaveTextContent('could not be refreshed');
});

test('background refresh is bounded while visible and leaves metadata usable after a temporary error', async () => {
  let statusCode = 204;
  let metadataCode = 200;
  let metadataCalls = 0;
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    if (String(input).endsWith('/status')) return new Response(null, { status: statusCode });
    metadataCalls++;
    return Response.json(metadataCode === 200 ? { model } : { error: 'temporary' }, { status: metadataCode });
  }));
  vi.useFakeTimers(); renderPage(); await tick(20);
  for (let i = 0; i < 5; i++) { fireEvent.focus(window); await tick(20); }
  expect(metadataCalls).toBe(1);
  const visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden');
  await tick(120_000);
  expect(metadataCalls).toBe(1);
  metadataCode = 503;
  visibility.mockReturnValue('visible'); fireEvent(document, new Event('visibilitychange')); await tick(20);
  expect(metadataCalls).toBe(2);
  expect(screen.getByRole('heading', { name: 'Original part' })).toBeInTheDocument();
  expect(screen.getByRole('alert')).toHaveTextContent('could not be refreshed');
  metadataCode = 200; fireEvent.click(screen.getByRole('button', { name: 'Retry' })); await tick(20);
  expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  expect(metadataCalls).toBe(3);
  statusCode = 500; await tick();
  expect(screen.getByRole('heading', { name: 'Original part' })).toBeInTheDocument();
  statusCode = 410; fireEvent.focus(window); await tick(20);
  expect(screen.getByText('Share not available')).toBeInTheDocument();
  expect(screen.queryByRole('link', { name: /part.stl/ })).not.toBeInTheDocument();
  await tick(120_000);
  expect(metadataCalls).toBe(3);
});

test('a temporary initial failure offers retry instead of claiming the share is gone', async () => {
  let fail = true;
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    if (String(input).endsWith('/status')) return new Response(null, { status: 204 });
    if (fail) throw new TypeError('network failed');
    return Response.json({ model });
  }));
  vi.useFakeTimers(); renderPage(); await tick(20);
  expect(screen.getByRole('alert')).toHaveTextContent('could not be loaded');
  expect(screen.queryByText('Share not available')).not.toBeInTheDocument();
  fail = false; fireEvent.click(screen.getByRole('button', { name: 'Retry' })); await tick(20);
  expect(screen.getByRole('heading', { name: 'Original part' })).toBeInTheDocument();
});

test('revocation aborts an overlapping metadata refresh and ignores its late response', async () => {
  let revoked = false;
  let finish: ((response: Response) => void) | undefined;
  let signal: AbortSignal | null | undefined;
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    if (String(input).endsWith('/status')) return new Response(null, { status: revoked ? 410 : 204 });
    if (String(input).includes('refresh=1')) { signal = init?.signal; return new Promise<Response>((resolve) => { finish = resolve; }); }
    return Response.json({ model });
  }));
  vi.useFakeTimers(); renderPage(); await tick(20); await tick();
  expect(finish).toBeDefined();
  revoked = true; fireEvent.focus(window); await tick(20);
  expect(signal?.aborted).toBe(true);
  expect(screen.getByText('Share not available')).toBeInTheDocument();
  await act(async () => { finish?.(Response.json({ model })); });
  expect(screen.queryByRole('heading', { name: 'Original part' })).not.toBeInTheDocument();
});

test('expiry hides a cached share without a network response, including while hidden', async () => {
  vi.useFakeTimers();
  const expiresAt = Math.floor(Date.now() / 1000) + 5;
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => String(input).endsWith('/status') ? new Response(null, { status: 204 }) : Response.json({ model, share: { expiresAt } })));
  renderPage(); await tick(20);
  expect(screen.getByRole('heading', { name: 'Original part' })).toBeInTheDocument();
  vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden');
  await tick(5000);
  expect(screen.getByText('Share not available')).toBeInTheDocument();
  expect(screen.queryByRole('heading', { name: 'Original part' })).not.toBeInTheDocument();
});

test('switching share tokens clears viewer approval and aborts old requests', async () => {
  const pending: AbortSignal[] = [];
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url.endsWith('/status')) return new Response(null, { status: 204 });
    if (url.startsWith('/api/public/token?') && url.includes('refresh=1')) { pending.push(init?.signal as AbortSignal); return new Promise<Response>(() => {}); }
    return Response.json({ model });
  }));
  vi.useFakeTimers(); const view = renderPage(); await tick(20);
  fireEvent.click(screen.getByRole('button', { name: 'Load 3D view' })); await tick(20);
  expect(screen.getByLabelText('Shared 3D view')).toBeInTheDocument();
  await tick();
  expect(pending).toHaveLength(1);
  act(() => { window.history.pushState({}, '', '/s/replacement?model=m1'); window.dispatchEvent(new Event('fileament:navigate')); });
  await tick(20);
  expect(pending[0].aborted).toBe(true);
  expect(screen.queryByLabelText('Shared 3D view')).not.toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Load 3D view' })).toBeInTheDocument();
  view.unmount();
});

test('a removed variant cannot transfer manual viewer approval to another large file', async () => {
  let removed = false;
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => String(input).endsWith('/status') ? new Response(null, { status: 204 }) : Response.json({ model: removed ? { ...model, files: [{ ...file, id: 'f2', filename: 'replacement.stl' }] } : model })));
  vi.useFakeTimers(); renderPage(); await tick(20);
  fireEvent.click(screen.getByRole('button', { name: 'Load 3D view' })); await tick(20);
  expect(screen.getByLabelText('Shared 3D view')).toBeInTheDocument();
  removed = true; await tick();
  expect(screen.queryByLabelText('Shared 3D view')).not.toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Load 3D view' })).toBeInTheDocument();
  expect(screen.getByRole('link', { name: /replacement.stl/ })).toBeInTheDocument();
});

test('stalled requests time out, retries do not overlap, and closing clears all timers', async () => {
  let stalled = true;
  const signals: AbortSignal[] = [];
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    if (String(input).endsWith('/status')) return new Response(null, { status: 204 });
    const signal = init?.signal as AbortSignal;
    signals.push(signal);
    if (stalled) return new Promise<Response>((_resolve, reject) => signal.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')), { once: true }));
    return Response.json({ model });
  }));
  vi.useFakeTimers(); const view = renderPage(); await tick(20);
  fireEvent.focus(window); await tick(20);
  expect(signals).toHaveLength(1);
  await tick(10_000);
  expect(signals[0].aborted).toBe(true);
  expect(screen.getByRole('alert')).toHaveTextContent('could not be loaded');
  stalled = false; fireEvent.click(screen.getByRole('button', { name: 'Retry' })); await tick(20);
  expect(screen.getByRole('heading', { name: 'Original part' })).toBeInTheDocument();
  stalled = true; await tick(120_000);
  view.unmount();
  expect(signals.every((signal) => signal.aborted || signal === signals[1])).toBe(true);
  expect(vi.getTimerCount()).toBe(0);
});

test('same-share history navigation resets selection state and uses the current query string', async () => {
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    if (String(input).endsWith('/status')) return new Response(null, { status: 204 });
    const selected = new URL(String(input), 'http://localhost').searchParams.get('model');
    return Response.json({ model: selected === 'm2' ? other : model, collection });
  }));
  vi.useFakeTimers(); renderPage(); await tick(20);
  expect(screen.getByRole('heading', { name: 'Original part' })).toBeInTheDocument();
  act(() => { window.history.pushState({}, '', '/s/token?model=m2'); window.dispatchEvent(new PopStateEvent('popstate')); });
  await tick(20);
  expect(screen.getByRole('heading', { name: 'Other part' })).toBeInTheDocument();
  expect(screen.queryByRole('link', { name: /part.stl/ })).not.toBeInTheDocument();
  act(() => { window.history.replaceState({}, '', '/s/token?model=m1'); window.dispatchEvent(new PopStateEvent('popstate')); });
  await tick(20);
  expect(screen.getByRole('heading', { name: 'Original part' })).toBeInTheDocument();
});
