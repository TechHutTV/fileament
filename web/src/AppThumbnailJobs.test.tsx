import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import '@testing-library/jest-dom/vitest';
import { afterEach, expect, test, vi } from 'vitest';
import { App } from './App';

afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); });

test('shows saved uploads with failed previews and retries without uploading again', async () => {
  window.history.pushState({}, '', '/upload');
  const listeners = new Map<string, (event: MessageEvent) => void>();
  vi.stubGlobal('EventSource', class {
    addEventListener(name: string, listener: (event: MessageEvent) => void) { listeners.set(name, listener); }
    close() {}
  });
  let status = 'failed';
  let uploads = 0;
  let retries = 0;
  const model = { id: 'm1', title: 'Saved cube', description: '', totalBytes: 123, files: [{ id: 'f1', modelId: 'm1', filename: 'cube.stl', relPath: 'files/cube.stl', format: 'stl', sizeBytes: 123, triangleCount: 12, bboxX: 1, bboxY: 1, bboxZ: 1 }] };
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url === '/api/me') return Response.json({ authenticated: true, setupRequired: false });
    if (url === '/api/collections') return Response.json([]);
    if (url === '/api/models' && init?.method === 'POST') { uploads++; return Response.json(model, { status: 201 }); }
    if (url === '/api/models/m1/thumbnails/retry') { retries++; status = 'pending'; return Response.json({ queued: 1 }); }
    if (url === '/api/models/m1') return Response.json({ ...model, primaryThumb: status === 'done' ? 'card.png' : '', thumbnailJobs: [{ fileId: 'f1', status, attempts: 1 }] });
    return Response.json({});
  }));
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })}><App /></QueryClientProvider>);
  fireEvent.click(await screen.findByLabelText(/Separate models/i));
  fireEvent.change(screen.getByLabelText('Choose 3D files'), { target: { files: [new File(['mesh'], 'cube.stl')] } });
  expect(await screen.findByText('Preview needs attention')).toBeInTheDocument();
  expect(screen.getByText('1 uploaded')).toBeInTheDocument();
  expect(screen.queryByText('Upload failed')).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole('button', { name: 'Retry previews' }));
  await waitFor(() => expect(retries).toBe(1));
  expect(await screen.findByText('Generating preview')).toBeInTheDocument();
  status = 'done';
  await act(async () => { listeners.get('thumbnail')?.(new MessageEvent('thumbnail', { data: JSON.stringify({ modelId: 'm1', fileId: 'f1', status: 'done' }) })); });
  expect(await screen.findByText('Ready')).toBeInTheDocument();
  expect(uploads).toBe(1);
  expect(screen.getByAltText('Saved cube thumbnail')).toBeInTheDocument();
});

test.each(['reconnect', 'poll'])('reconciles mixed failed and pending variant previews after %s', async (trigger) => {
  window.history.pushState({}, '', '/upload');
  const listeners = new Map<string, () => void>();
  vi.stubGlobal('EventSource', class {
    addEventListener(name: string, listener: () => void) { listeners.set(name, listener); }
    close() {}
  });
  let poll = () => undefined;
  const setInterval = window.setInterval.bind(window);
  vi.spyOn(window, 'setInterval').mockImplementation((handler, timeout, ...args) => {
    if (timeout === 15_000 && typeof handler === 'function') poll = () => { handler(); };
    return setInterval(handler, timeout, ...args);
  });
  const file = { modelId: 'm1', relPath: 'files/part.stl', format: 'stl', sizeBytes: 123, triangleCount: 12, bboxX: 1, bboxY: 1, bboxZ: 1 };
  const model = { id: 'm1', title: 'Variants', description: '', totalBytes: 246, files: [{ ...file, id: 'f1', filename: 'first.stl' }, { ...file, id: 'f2', filename: 'second.stl' }] };
  let secondDone = false;
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url === '/api/me') return Response.json({ authenticated: true, setupRequired: false });
    if (url === '/api/collections') return Response.json([]);
    if (url === '/api/models/grouped' && init?.method === 'POST') return Response.json(model, { status: 201 });
    if (url === '/api/models/m1') return Response.json({ ...model,
      files: model.files.map((variant) => ({ ...variant, thumbPath: variant.id === 'f2' && secondDone ? 'thumbs/f2.png' : '' })),
      thumbnailJobs: [{ fileId: 'f1', status: 'failed', attempts: 1 }, { fileId: 'f2', status: secondDone ? 'done' : 'pending', attempts: 0 }],
    });
    return Response.json({});
  }));
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><App /></QueryClientProvider>);
  fireEvent.click(await screen.findByLabelText(/One model with variants/i));
  fireEvent.change(screen.getByLabelText('Choose 3D files'), { target: { files: [new File(['mesh'], 'first.stl'), new File(['mesh'], 'second.stl')] } });
  expect(await screen.findByText('Preview needs attention')).toBeInTheDocument();
  expect(screen.getByLabelText('first.stl preview unavailable')).not.toHaveClass('loading');
  expect(screen.getByLabelText('second.stl preview rendering')).toHaveClass('loading');
  secondDone = true;
  await act(async () => { if (trigger === 'reconnect') listeners.get('open')?.(); else poll(); });
  expect(await screen.findByAltText('second.stl variant preview')).toBeInTheDocument();
  expect(screen.getByLabelText('first.stl preview unavailable')).toBeInTheDocument();
  expect(screen.getByText('1 uploaded')).toBeInTheDocument();
});

test('keeps model downloads available when retry fails and allows another attempt', async () => {
  window.history.pushState({}, '', '/models/m1');
  vi.stubGlobal('EventSource', class { addEventListener() {} close() {} });
  let status = 'failed';
  let attempts = 0;
  const model = { id: 'm1', title: 'Saved cube', description: '', totalBytes: 60 * 1024 * 1024, files: [{ id: 'f1', modelId: 'm1', filename: 'cube.stl', relPath: 'files/cube.stl', format: 'stl', sizeBytes: 60 * 1024 * 1024, triangleCount: 12, bboxX: 1, bboxY: 1, bboxZ: 1 }] };
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url === '/api/me') return Response.json({ authenticated: true, setupRequired: false });
    if (url === '/api/models/m1/thumbnails/retry') {
      if (++attempts === 1) return Response.json({ error: 'retry failed' }, { status: 503 });
      status = 'pending';
      return Response.json({ queued: 1 });
    }
    if (url === '/api/models/m1') return Response.json({ ...model, thumbnailJobs: [{ fileId: 'f1', status, attempts: 1 }] });
    return Response.json([]);
  }));
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })}><App /></QueryClientProvider>);
  expect(await screen.findByRole('alert')).toHaveTextContent('Your model is saved.');
  fireEvent.click(screen.getByRole('button', { name: 'Retry previews' }));
  expect(await screen.findByText('Could not retry the preview. Try again shortly.')).toHaveAttribute('role', 'alert');
  expect(screen.getByRole('link', { name: /Download cube.stl/ })).toHaveAttribute('href', '/files/m1/f1');
  fireEvent.click(screen.getByRole('button', { name: 'Retry previews' }));
  expect(await screen.findByRole('status')).toHaveTextContent('Generating previews.');
  expect(screen.queryByRole('button', { name: 'Retry previews' })).not.toBeInTheDocument();
  expect(attempts).toBe(2);
});
