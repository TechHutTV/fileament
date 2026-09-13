import { focusManager, QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import '@testing-library/jest-dom/vitest';
import { StrictMode, useEffect } from 'react';
import { afterEach, expect, test, vi } from 'vitest';
import { App } from './App';

const { disposeViewer } = vi.hoisted(() => ({ disposeViewer: vi.fn() }));
vi.mock('./Viewer', () => ({ default: function Viewer() {
  useEffect(() => () => disposeViewer(), []);
  return <div aria-label="Private 3D view" />;
} }));
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); vi.useRealTimers(); focusManager.setFocused(undefined); });

const file = { id: 'f1', modelId: 'm1', filename: 'part.stl', relPath: 'files/part.stl', format: 'stl', sizeBytes: 60 * 1024 * 1024, triangleCount: 1, bboxX: 1, bboxY: 1, bboxZ: 1 };
const model = { id: 'm1', title: 'Original library', description: '', totalBytes: file.sizeBytes, files: [file], images: [] };

function renderApp(path = '/models/m1', strict = false) {
  window.history.replaceState({}, '', path);
  vi.stubGlobal('EventSource', class { addEventListener = vi.fn(); close = vi.fn(); });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const app = <QueryClientProvider client={client}><App /></QueryClientProvider>;
  render(strict ? <StrictMode>{app}</StrictMode> : app);
  return client;
}

test('a replacement session clears private metadata and manual viewer approval', async () => {
  let context = 'first-session';
  let finish: ((response: Response) => void) | undefined;
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    if (String(input) === '/api/me') return Response.json({ authenticated: true, context });
    if (String(input) === '/api/models/m1') {
      if (context === 'second-session') return new Promise<Response>((resolve) => { finish = resolve; });
      return Response.json(model);
    }
    return Response.json([]);
  }));
  const client = renderApp();
  fireEvent.click(await screen.findByRole('button', { name: 'Load 3D view' }));
  expect(await screen.findByLabelText('Private 3D view')).toBeInTheDocument();
  context = 'second-session';
  await act(async () => { await client.refetchQueries({ queryKey: ['me'] }); });
  await waitFor(() => expect(finish).toBeDefined());
  expect(screen.queryByDisplayValue('Original library')).not.toBeInTheDocument();
  expect(screen.queryByLabelText('Private 3D view')).not.toBeInTheDocument();
  expect(disposeViewer).toHaveBeenCalled();
  await act(async () => { finish?.(Response.json({ ...model, title: 'Restored library' })); });
  expect(await screen.findByDisplayValue('Restored library')).toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Load 3D view' })).toBeInTheDocument();
});

test('a save from an ended session cannot update the replacement session', async () => {
  let context = 'first-session';
  let finish: ((response: Response) => void) | undefined;
  let signal: AbortSignal | null | undefined;
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    if (String(input) === '/api/me') return Response.json({ authenticated: true, context });
    if (String(input) === '/api/models/m1') {
      if (init?.method === 'PATCH') {
        signal = init.signal;
        return new Promise<Response>((resolve) => { finish = resolve; });
      }
      return Response.json(context === 'first-session' ? model : { ...model, title: 'Restored library' });
    }
    return Response.json([]);
  }));
  const client = renderApp();
  fireEvent.change(await screen.findByLabelText('Title'), { target: { value: 'Old save' } });
  fireEvent.click(screen.getByRole('button', { name: 'Save changes' }));
  await screen.findByRole('button', { name: 'Saving changes…' });
  context = 'second-session';
  await act(async () => { await client.refetchQueries({ queryKey: ['me'] }); });
  const title = await screen.findByDisplayValue('Restored library');
  expect(signal?.aborted).toBe(true);
  fireEvent.change(title, { target: { value: 'New draft' } });
  await act(async () => { finish?.(Response.json({ ...model, title: 'Old save' })); });
  expect(title).toHaveValue('New draft');
  expect(screen.queryByText('Changes saved.')).not.toBeInTheDocument();
});

test('successful restore removes private state before a delayed session refresh', async () => {
  let restored = false;
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input);
    if (path === '/api/me') return restored ? new Promise<Response>(() => {}) : Response.json({ authenticated: true, context: 'first-session' });
    if (path === '/api/storage') return Response.json({ totalBytes: 1 });
    if (path === '/api/backups/inspect') return Response.json({ restoreToken: 'review-token', manifest: { models: 1, files: 1, collections: 0, createdAt: '2026-09-12T00:00:00Z' } });
    if (path === '/api/backups/restore' && init?.method === 'POST') { restored = true; return Response.json({ ok: true }); }
    return Response.json([]);
  }));
  const client = renderApp('/settings');
  fireEvent.change(await screen.findByLabelText('Fileament backup'), { target: { files: [new File(['backup'], 'library.fileament')] } });
  fireEvent.click(screen.getByRole('button', { name: 'Review backup' }));
  fireEvent.change(await screen.findByLabelText('Type RESTORE to confirm'), { target: { value: 'RESTORE' } });
  fireEvent.click(screen.getByRole('button', { name: 'Replace current data' }));
  expect(await screen.findByText('Owner login')).toBeInTheDocument();
  expect(screen.queryByLabelText('Fileament backup')).not.toBeInTheDocument();
  await act(async () => { await client.cancelQueries(); });
});

test('signing in again cannot display cached metadata from the previous session', async () => {
  let authenticated = true;
  let loggedInAgain = false;
  let finish: ((response: Response) => void) | undefined;
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const path = String(input);
    if (path === '/api/me') return Response.json({ authenticated, context: authenticated ? loggedInAgain ? 'second-session' : 'first-session' : undefined });
    if (path === '/api/auth/login') { authenticated = true; loggedInAgain = true; return Response.json({ ok: true }); }
    if (path === '/api/models/m1') return loggedInAgain ? new Promise<Response>((resolve) => { finish = resolve; }) : Response.json(model);
    return Response.json([]);
  }));
  const client = renderApp();
  await screen.findByDisplayValue('Original library');
  authenticated = false;
  await act(async () => { await client.refetchQueries({ queryKey: ['me'] }); });
  fireEvent.change(await screen.findByLabelText('Password'), { target: { value: 'restored-password' } });
  fireEvent.click(screen.getByRole('button', { name: 'Log in' }));
  await waitFor(() => expect(finish).toBeDefined());
  expect(screen.queryByDisplayValue('Original library')).not.toBeInTheDocument();
  await act(async () => { finish?.(Response.json({ ...model, title: 'Restored library' })); });
  expect(await screen.findByDisplayValue('Restored library')).toBeInTheDocument();
});

test('failed restore preserves the current session and review for retry', async () => {
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const path = String(input);
    if (path === '/api/me') return Response.json({ authenticated: true, context: 'first-session' });
    if (path === '/api/storage') return Response.json({ totalBytes: 1 });
    if (path === '/api/backups/inspect') return Response.json({ restoreToken: 'review-token', manifest: { models: 1, files: 1, collections: 0, createdAt: '2026-09-12T00:00:00Z' } });
    if (path === '/api/backups/restore') return Response.json({ error: 'restore failed' }, { status: 500 });
    return Response.json([]);
  }));
  renderApp('/settings');
  fireEvent.change(await screen.findByLabelText('Fileament backup'), { target: { files: [new File(['backup'], 'library.fileament')] } });
  fireEvent.click(screen.getByRole('button', { name: 'Review backup' }));
  fireEvent.change(await screen.findByLabelText('Type RESTORE to confirm'), { target: { value: 'RESTORE' } });
  fireEvent.click(screen.getByRole('button', { name: 'Replace current data' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('Restore failed.');
  expect(screen.getByLabelText('Type RESTORE to confirm')).toHaveValue('RESTORE');
  expect(screen.getByRole('button', { name: 'Replace current data' })).toBeEnabled();
  expect(screen.getByRole('link', { name: 'Upload' })).toBeInTheDocument();
});

test('a private 401 removes the owner page while a rejected password leaves it available', async () => {
  let expired = false;
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const path = String(input);
    if (path === '/api/me') return Response.json({ authenticated: !expired, context: expired ? undefined : 'first-session' });
    if (path === '/api/auth/password') return Response.json({ error: 'invalid current password' }, { status: 401 });
    if (path === '/api/storage') return Response.json({ totalBytes: 1 });
    if (path === '/api/models/m1') { expired = true; return Response.json({ error: 'authentication required' }, { status: 401 }); }
    return Response.json([]);
  }));
  renderApp('/settings');
  fireEvent.change(await screen.findByLabelText('Current password'), { target: { value: 'wrong-password' } });
  fireEvent.change(screen.getByLabelText('New password'), { target: { value: 'replacement-password' } });
  fireEvent.click(screen.getByRole('button', { name: 'Change password' }));
  expect(await screen.findByText('Password change failed')).toBeInTheDocument();
  expect(screen.getByRole('link', { name: 'Upload' })).toBeInTheDocument();
  await act(async () => { window.history.pushState({}, '', '/models/m1'); window.dispatchEvent(new Event('fileament:navigate')); });
  expect(await screen.findByText('Owner login')).toBeInTheDocument();
});

test('session checks retry after a network failure and keep a verified draft through a transient failure', async () => {
  let unavailable = true;
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    if (String(input) === '/api/me') {
      if (unavailable) throw new TypeError('Network unavailable');
      return Response.json({ authenticated: true, context: 'first-session' });
    }
    if (String(input) === '/api/models/m1') return Response.json(model);
    return Response.json([]);
  }));
  const client = renderApp();
  expect(await screen.findByRole('alert')).toHaveTextContent('Your session could not be checked.');
  unavailable = false;
  fireEvent.click(screen.getByRole('button', { name: 'Retry session check' }));
  const title = await screen.findByLabelText('Title');
  fireEvent.change(title, { target: { value: 'Unsaved draft' } });
  unavailable = true;
  await act(async () => { await client.refetchQueries({ queryKey: ['me'] }); });
  expect(title).toHaveValue('Unsaved draft');
  expect(screen.queryByText('Owner login')).not.toBeInTheDocument();
});

test('periodic session checks detect replacement without a navigation', async () => {
  vi.useFakeTimers();
  focusManager.setFocused(true);
  let context = 'first-session';
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    if (String(input) === '/api/me') return Response.json({ authenticated: true, context });
    if (String(input) === '/api/models/m1') return Response.json({ ...model, title: context });
    return Response.json([]);
  }));
  renderApp();
  await act(async () => { await vi.advanceTimersByTimeAsync(100); });
  expect(screen.getByLabelText('Title')).toHaveValue('first-session');
  context = 'second-session';
  await act(async () => { await vi.advanceTimersByTimeAsync(60_100); });
  expect(screen.getByLabelText('Title')).toHaveValue('second-session');
});

test('StrictMode remounts retain a usable session and final unmount aborts requests', async () => {
  let finish: ((response: Response) => void) | undefined;
  let signal: AbortSignal | null | undefined;
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    if (String(input) === '/api/me') return Response.json({ authenticated: true, context: 'first-session' });
    if (String(input) === '/api/models/m1') {
      if (init?.method === 'PATCH') { signal = init.signal; return new Promise<Response>((resolve) => { finish = resolve; }); }
      return Response.json(model);
    }
    return Response.json([]);
  }));
  const client = renderApp('/models/m1', true);
  await screen.findByLabelText('Title');
  fireEvent.click(screen.getByRole('button', { name: 'Save changes' }));
  await screen.findByRole('button', { name: 'Saving changes…' });
  await act(async () => { client.setQueryData(['me'], { authenticated: false }); });
  expect(await screen.findByText('Owner login')).toBeInTheDocument();
  expect(signal?.aborted).toBe(true);
  await act(async () => { finish?.(Response.json(model)); });
});

test('ending an upload session aborts active uploads without assigning or deleting models in a new context', async () => {
  let context = 'first-session';
  const uploads: { signal: AbortSignal; finish: () => void }[] = [];
  const writes: string[] = [];
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input);
    if (path === '/api/me') return Response.json({ authenticated: true, context });
    if (path === '/api/collections') return Response.json([{ id: 'c1', name: 'Parts', slug: 'parts', modelCount: 0 }]);
    if (path === '/api/models' && init?.method === 'POST') {
      return new Promise<Response>((resolve) => { uploads.push({ signal: init.signal!, finish: () => resolve(Response.json(model)) }); });
    }
    if (init?.method === 'PUT' || init?.method === 'DELETE') writes.push(path);
    return Response.json([]);
  }));
  const client = renderApp('/upload');
  fireEvent.click(await screen.findByLabelText(/Separate models/i));
  fireEvent.change(screen.getByLabelText('Add uploads to collection'), { target: { value: 'c1' } });
  fireEvent.change(screen.getByLabelText('Choose 3D files'), { target: { files: ['one', 'two', 'three', 'queued'].map((name) => new File(['mesh'], name + '.stl')) } });
  await waitFor(() => expect(uploads).toHaveLength(3));
  context = 'second-session';
  await act(async () => { await client.refetchQueries({ queryKey: ['me'] }); });
  await waitFor(() => expect(uploads.every((upload) => upload.signal.aborted)).toBe(true));
  await act(async () => { uploads.forEach((upload) => upload.finish()); });
  expect(uploads).toHaveLength(3);
  expect(writes).toEqual([]);
  expect(screen.queryByText('Original library')).not.toBeInTheDocument();
});

test('a stale page cannot save after its cookie changes before the next session check', async () => {
  let cookieContext = 'first-session';
  let writes = 0;
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input);
    if (path === '/api/me') return Response.json({ authenticated: true, context: cookieContext });
    if (path === '/api/models/m1') {
      if (new Headers(init?.headers).get('X-Fileament-Context') !== cookieContext) return Response.json({ error: 'authentication required' }, { status: 401 });
      if (init?.method === 'PATCH') writes++;
      return Response.json(cookieContext === 'first-session' ? model : { ...model, title: 'Replacement library' });
    }
    return Response.json([]);
  }));
  renderApp();
  const title = await screen.findByLabelText('Title');
  cookieContext = 'second-session';
  fireEvent.change(title, { target: { value: 'Edit from stale page' } });
  fireEvent.click(screen.getByRole('button', { name: 'Save changes' }));
  expect(await screen.findByDisplayValue('Replacement library')).toBeInTheDocument();
  expect(writes).toBe(0);
  expect(screen.queryByDisplayValue('Edit from stale page')).not.toBeInTheDocument();
});

test('password rotation clears the old backup link and retains success feedback', async () => {
  let context = 'first-session';
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const path = String(input);
    if (path === '/api/me') return Response.json({ authenticated: true, context });
    if (path === '/api/storage') return Response.json({ totalBytes: 1 });
    if (path === '/api/backups/prepare') return Response.json({ downloadUrl: '/api/backups/download/old', filename: 'library.fileament', sizeBytes: 1, expiresAt: Math.floor(Date.now() / 1000) + 900 });
    if (path === '/api/auth/password') { context = 'second-session'; return new Response(null, { status: 204 }); }
    return Response.json([]);
  }));
  renderApp('/settings');
  fireEvent.click(await screen.findByRole('button', { name: 'Create backup' }));
  expect(await screen.findByRole('link', { name: /Download backup/ })).toBeInTheDocument();
  fireEvent.change(screen.getByLabelText('Current password'), { target: { value: 'current-password' } });
  fireEvent.change(screen.getByLabelText('New password'), { target: { value: 'replacement-password' } });
  fireEvent.click(screen.getByRole('button', { name: 'Change password' }));
  await waitFor(() => expect(screen.queryByRole('link', { name: /Download backup/ })).not.toBeInTheDocument());
  expect(screen.getByRole('status')).toHaveTextContent('Password updated.');
  expect(screen.getByLabelText('Current password')).toHaveValue('');
  expect(screen.getByLabelText('New password')).toHaveValue('');
});
