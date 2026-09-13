import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import '@testing-library/jest-dom/vitest';
import { afterEach, expect, test, vi } from 'vitest';
import { App } from './App';

afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); });

test('coalesces a thumbnail burst into one catalog refresh and preserves the server-selected cover', async () => {
  window.history.pushState({}, '', '/');
  const listeners = new Map<string, (event: MessageEvent) => void>();
  const close = vi.fn();
  vi.stubGlobal('EventSource', class {
    addEventListener(name: string, listener: (event: MessageEvent) => void) { listeners.set(name, listener); }
    close = close;
  });
  let catalogRequests = 0;
  let primaryThumb = '';
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url === '/api/me') return Response.json({ authenticated: true });
    if (url.startsWith('/api/models?')) {
      catalogRequests++;
      return Response.json({ items: [{ id: 'm1', title: 'Chosen cover', primaryThumb, totalBytes: 1, files: [] }], nextCursor: '' });
    }
    return Response.json([]);
  }));
  const { unmount } = render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><App /></QueryClientProvider>);
  expect(await screen.findByRole('link', { name: /Chosen cover/ })).toBeInTheDocument();
  expect(catalogRequests).toBe(1);
  primaryThumb = 'selected.png';
  await act(async () => {
    for (let i = 0; i < 100; i++) listeners.get('thumbnail')?.(new MessageEvent('thumbnail', { data: JSON.stringify({ modelId: 'm1', fileId: 'other-'+i, thumbPath: 'thumbs/other.png', status: 'done' }) }));
  });
  expect(catalogRequests).toBe(1);
  await waitFor(() => expect(catalogRequests).toBe(2));
  expect(await screen.findByAltText('Chosen cover thumbnail')).toHaveAttribute('src', '/thumbs/m1/selected.png');
  unmount();
  expect(close).toHaveBeenCalledTimes(1);
});

test('refreshes the affected detail without discarding an unsaved title', async () => {
  window.history.pushState({}, '', '/models/m1');
  const listeners = new Map<string, (event: MessageEvent) => void>();
  vi.stubGlobal('EventSource', class {
    addEventListener(name: string, listener: (event: MessageEvent) => void) { listeners.set(name, listener); }
    close() {}
  });
  let requests = 0;
  let ready = false;
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url === '/api/me') return Response.json({ authenticated: true });
    if (url === '/api/models/m1') {
      requests++;
      return Response.json({ id: 'm1', title: 'Saved title', description: '', totalBytes: 60 * 1024 * 1024, primaryThumb: ready ? 'selected.png' : '',
        files: [{ id: 'f1', modelId: 'm1', filename: 'cube.stl', relPath: 'files/cube.stl', format: 'stl', sizeBytes: 60 * 1024 * 1024, triangleCount: 12, bboxX: 1, bboxY: 1, bboxZ: 1 }],
        thumbnailJobs: [{ fileId: 'f1', status: ready ? 'done' : 'pending', attempts: 1 }],
      });
    }
    return Response.json([]);
  }));
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><App /></QueryClientProvider>);
  fireEvent.change(await screen.findByLabelText('Title'), { target: { value: 'Draft title' } });
  ready = true;
  await act(async () => {
    for (let i = 0; i < 100; i++) listeners.get('thumbnail')?.(new MessageEvent('thumbnail', { data: JSON.stringify({ modelId: 'm1' }) }));
  });
  expect(requests).toBe(1);
  await waitFor(() => expect(screen.queryByText('Generating previews. Your model files are available to download.')).not.toBeInTheDocument());
  expect(requests).toBe(2);
  expect(screen.getByLabelText('Title')).toHaveValue('Draft title');
  expect(screen.getByAltText('cube.stl preview')).toHaveAttribute('src', '/thumbs/m1/selected.png');
});

test('bounds upload preview refresh requests and aborts them when the page closes', async () => {
  window.history.pushState({}, '', '/upload');
  const listeners = new Map<string, (event: MessageEvent) => void>();
  vi.stubGlobal('EventSource', class {
    addEventListener(name: string, listener: (event: MessageEvent) => void) { listeners.set(name, listener); }
    close() {}
  });
  const model = (id: string, status = 'pending') => ({ id, title: id, description: '', totalBytes: 1,
    files: [{ id: id+'-file', modelId: id, filename: id+'.stl', relPath: 'files/part.stl', format: 'stl', sizeBytes: 1, triangleCount: 1, bboxX: 1, bboxY: 1, bboxZ: 1 }],
    thumbnailJobs: [{ fileId: id+'-file', status, attempts: 1 }],
  });
  let deferRefresh = false;
  let active = 0;
  let peak = 0;
  const requests: { signal: AbortSignal; finish: () => void }[] = [];
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url === '/api/me') return Response.json({ authenticated: true });
    if (url === '/api/models' && init?.method === 'POST') {
      const file = (init.body as FormData).get('file') as File;
      return Response.json(model(file.name), { status: 201 });
    }
    if (url.startsWith('/api/models/')) {
      const id = url.split('/').at(-1)!;
      if (!deferRefresh) return Response.json(model(id));
      const signal = init!.signal!;
      return new Promise<Response>((resolve, reject) => {
        active++;
        peak = Math.max(peak, active);
        let settled = false;
        const abort = () => { if (!settled) { settled = true; active--; reject(new DOMException('Canceled', 'AbortError')); } };
        signal.addEventListener('abort', abort, { once: true });
        requests.push({ signal, finish: () => {
          if (settled) return;
          settled = true;
          active--;
          signal.removeEventListener('abort', abort);
          resolve(Response.json(model(id, 'done')));
        } });
      });
    }
    return Response.json([]);
  }));
  const { unmount } = render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><App /></QueryClientProvider>);
  fireEvent.click(await screen.findByLabelText(/Separate models/i));
  const files = Array.from({ length: 5 }, (_, i) => new File(['mesh'], 'part'+i+'.stl'));
  fireEvent.change(screen.getByLabelText('Choose 3D files'), { target: { files } });
  expect(await screen.findByText('5 uploaded')).toBeInTheDocument();
  deferRefresh = true;
  await act(async () => {
    for (const file of files) listeners.get('thumbnail')?.(new MessageEvent('thumbnail', { data: JSON.stringify({ modelId: file.name }) }));
  });
  await waitFor(() => expect(requests).toHaveLength(3));
  expect(peak).toBe(3);
  await act(async () => requests[0].finish());
  await waitFor(() => expect(requests).toHaveLength(4));
  expect(peak).toBe(3);
  unmount();
  await waitFor(() => expect(active).toBe(0));
  expect(requests).toHaveLength(4);
  expect(requests.slice(1).every((request) => request.signal.aborted)).toBe(true);
});
