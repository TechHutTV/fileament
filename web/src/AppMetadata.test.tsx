import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import '@testing-library/jest-dom/vitest';
import { afterEach, expect, test, vi } from 'vitest';
import { App } from './App';

afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); });

const model = { id: 'm1', title: 'Part', description: 'Original notes', sourceUrl: '', license: '', author: '', tags: ['tools'], totalBytes: 0, files: [], images: [] };
const collection = { id: 'c1', slug: 'parts', name: 'Parts', description: 'Original description', coverModelId: '', modelCount: 0, models: [] };

function renderPage(path: string) {
  window.history.replaceState({}, '', path);
  vi.stubGlobal('EventSource', class { addEventListener = vi.fn(); close = vi.fn(); });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  render(<QueryClientProvider client={client}><App /></QueryClientProvider>);
  return client;
}

test('model save prevents duplicate submissions and preserves newer edits', async () => {
  let current = model;
  let finish: ((value: Response) => void) | undefined;
  const saved: Record<string, unknown>[] = [];
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input);
    if (path === '/api/me') return Response.json({ authenticated: true });
    if (path === '/api/models/m1' && init?.method === 'PATCH') {
      const values = JSON.parse(String(init.body));
      saved.push(values);
      current = { ...current, ...values };
      return new Promise<Response>((resolve) => { finish = resolve; });
    }
    if (path === '/api/models/m1') return Response.json(current);
    return Response.json([]);
  }));
  renderPage('/models/m1');
  const title = await screen.findByLabelText('Title');
  fireEvent.change(title, { target: { value: 'First edit' } });
  fireEvent.click(screen.getByRole('button', { name: 'Save changes' }));
  const saving = await screen.findByRole('button', { name: 'Saving changes…' });
  expect(saving).toBeDisabled();
  fireEvent.submit(saving.closest('form')!);
  fireEvent.change(title, { target: { value: 'Newer edit' } });
  await act(async () => { finish?.(Response.json(current)); });
  expect(await screen.findByRole('status')).toHaveTextContent('Saved. You have unsaved changes.');
  expect(title).toHaveValue('Newer edit');
  expect(saved).toHaveLength(1);
  fireEvent.click(screen.getByRole('button', { name: 'Save changes' }));
  await waitFor(() => expect(saved).toHaveLength(2));
  expect(saved[1].title).toBe('Newer edit');
  await act(async () => { finish?.(Response.json(current)); });
  expect(await screen.findByRole('status')).toHaveTextContent('Changes saved.');
});

test('collection refresh merges clean fields without replacing an unsaved name', async () => {
  let current = collection;
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const path = String(input);
    if (path === '/api/me') return Response.json({ authenticated: true });
    if (path === '/api/collections/parts') return Response.json(current);
    return Response.json([]);
  }));
  const client = renderPage('/collections/parts');
  const name = await screen.findByLabelText('Collection name');
  fireEvent.change(name, { target: { value: 'Unsaved collection' } });
  current = { ...current, name: 'Remote name', description: 'Remote description' };
  await act(async () => { await client.refetchQueries({ queryKey: ['collection', 'parts'] }); });
  await waitFor(() => expect(screen.getByLabelText('Collection description')).toHaveValue('Remote description'));
  expect(name).toHaveValue('Unsaved collection');
});

test.each(['model', 'collection'])('%s save failure retains edits and permits a retry', async (kind) => {
  let fail = true;
  let current = kind === 'model' ? model : collection;
  const path = kind === 'model' ? '/api/models/m1' : '/api/collections/parts';
  const field = kind === 'model' ? 'Title' : 'Collection name';
  const button = kind === 'model' ? 'Save changes' : 'Save collection';
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    if (String(input) === '/api/me') return Response.json({ authenticated: true });
    if (init?.method === 'PATCH') {
      if (fail) return Response.json({ error: 'temporary failure' }, { status: 500 });
      current = { ...current, ...JSON.parse(String(init.body)) };
      return Response.json(current);
    }
    if (String(input) === path) return Response.json(current);
    return Response.json([]);
  }));
  renderPage(kind === 'model' ? '/models/m1' : '/collections/parts');
  fireEvent.change(await screen.findByLabelText(field), { target: { value: 'Keep this edit' } });
  fireEvent.click(screen.getByRole('button', { name: button }));
  expect(await screen.findByRole('alert')).toHaveTextContent('Could not save. Your edits are still here; try again.');
  expect(screen.getByLabelText(field)).toHaveValue('Keep this edit');
  expect(screen.getByRole('button', { name: button })).toBeEnabled();
  fail = false;
  fireEvent.click(screen.getByRole('button', { name: button }));
  expect(await screen.findByRole('status')).toHaveTextContent('Changes saved.');
  expect(screen.queryByRole('alert')).not.toBeInTheDocument();
});

test('collection rename preserves newer edits, focus, and loaded member pages', async () => {
  const first = { ...model, title: 'First member' };
  const second = { ...model, id: 'm2', title: 'Second member' };
  const current = { ...collection, modelCount: 2, models: [first], nextCursor: 'two' };
  let finish: ((value: Response) => void) | undefined;
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input);
    if (path === '/api/me') return Response.json({ authenticated: true });
    if (init?.method === 'PATCH') return new Promise<Response>((resolve) => { finish = resolve; });
    if (path === '/api/collections/parts') return Response.json(current);
    if (path === '/api/collections/parts?cursor=two') return Response.json({ ...current, models: [second], nextCursor: '' });
    if (path.startsWith('/api/collections/renamed')) return new Promise<Response>(() => {});
    return Response.json([]);
  }));
  const client = renderPage('/collections/parts');
  fireEvent.click(await screen.findByRole('button', { name: 'Load more models' }));
  expect(await screen.findByRole('link', { name: /Second member/ })).toBeInTheDocument();
  const name = screen.getByLabelText('Collection name');
  fireEvent.change(name, { target: { value: 'Renamed' } });
  fireEvent.click(screen.getByRole('button', { name: 'Save collection' }));
  expect(await screen.findByRole('button', { name: 'Saving collection…' })).toBeDisabled();
  fireEvent.change(name, { target: { value: 'A newer name' } });
  name.focus();
  await act(async () => { finish?.(Response.json({ ...current, name: 'Renamed', slug: 'renamed' })); });
  await waitFor(() => expect(window.location.pathname).toBe('/collections/renamed'));
  expect(await screen.findByRole('status')).toHaveTextContent('Saved. You have unsaved changes.');
  expect(screen.getByLabelText('Collection name')).toBe(name);
  expect(name).toHaveFocus();
  expect(name).toHaveValue('A newer name');
  expect(screen.getByRole('link', { name: /Second member/ })).toBeInTheDocument();
  expect(screen.getByText('2 of 2 models shown')).toBeInTheDocument();
  await act(async () => { await client.cancelQueries(); });
});

test('creating a collection clears submitted fields and retains the next draft', async () => {
  let finish: ((value: Response) => void) | undefined;
  let count = 0;
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    if (String(input) === '/api/me') return Response.json({ authenticated: true });
    if (init?.method === 'POST') {
      count++;
      return new Promise<Response>((resolve) => { finish = resolve; });
    }
    return Response.json([]);
  }));
  renderPage('/collections');
  const name = await screen.findByLabelText('Name');
  fireEvent.change(name, { target: { value: 'First collection' } });
  fireEvent.change(screen.getByLabelText('Description'), { target: { value: 'First description' } });
  fireEvent.click(screen.getByRole('button', { name: 'Create collection' }));
  const saving = await screen.findByRole('button', { name: 'Creating collection…' });
  expect(saving).toBeDisabled();
  fireEvent.submit(saving.closest('form')!);
  fireEvent.change(name, { target: { value: 'Next collection' } });
  await act(async () => { finish?.(Response.json(collection)); });
  expect(await screen.findByRole('status')).toHaveTextContent('Saved. You have unsaved changes.');
  expect(name).toHaveValue('Next collection');
  expect(screen.getByLabelText('Description')).toHaveValue('');
  expect(count).toBe(1);
});

test('a stale model response cannot overwrite the acknowledged save', async () => {
  let current = model;
  let stale: ((value: Response) => void) | undefined;
  let reads = 0;
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    if (String(input) === '/api/me') return Response.json({ authenticated: true });
    if (String(input) === '/api/models/m1') {
      if (init?.method === 'PATCH') {
        current = { ...current, ...JSON.parse(String(init.body)), title: 'Normalized title' };
        return Response.json(current);
      }
      if (++reads === 2) return new Promise<Response>((resolve) => { stale = resolve; });
      return Response.json(current);
    }
    return Response.json([]);
  }));
  const client = renderPage('/models/m1');
  const title = await screen.findByLabelText('Title');
  void client.refetchQueries({ queryKey: ['model', 'm1'] });
  await waitFor(() => expect(stale).toBeDefined());
  fireEvent.change(title, { target: { value: '  Normalized title  ' } });
  fireEvent.click(screen.getByRole('button', { name: 'Save changes' }));
  expect(await screen.findByRole('status')).toHaveTextContent('Changes saved.');
  expect(title).toHaveValue('Normalized title');
  await act(async () => { stale?.(Response.json(model)); });
  expect(title).toHaveValue('Normalized title');
});

test('an earlier collection save cannot redirect a later navigation or copy its members', async () => {
  let finish: ((value: Response) => void) | undefined;
  const other = { ...collection, id: 'c2', name: 'Other collection', slug: 'other', models: [{ ...model, id: 'm2', title: 'Other member' }], modelCount: 1 };
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input);
    if (path === '/api/me') return Response.json({ authenticated: true });
    if (init?.method === 'PATCH') return new Promise<Response>((resolve) => { finish = resolve; });
    if (path === '/api/collections/parts') return Response.json(collection);
    if (path === '/api/collections/other') return Response.json(other);
    return Response.json([]);
  }));
  const client = renderPage('/collections/parts');
  fireEvent.change(await screen.findByLabelText('Collection name'), { target: { value: 'Renamed' } });
  fireEvent.click(screen.getByRole('button', { name: 'Save collection' }));
  await screen.findByRole('button', { name: 'Saving collection…' });
  await act(async () => {
    window.history.pushState({}, '', '/collections/other');
    window.dispatchEvent(new Event('fileament:navigate'));
  });
  expect(await screen.findByDisplayValue('Other collection')).toBeInTheDocument();
  await act(async () => { finish?.(Response.json({ ...collection, name: 'Renamed', slug: 'renamed' })); });
  expect(window.location.pathname).toBe('/collections/other');
  expect(screen.getByLabelText('Collection name')).toHaveValue('Other collection');
  expect(client.getQueryData(['collection', 'renamed'])).toMatchObject({ pages: [{ id: 'c1', models: [] }] });
});
