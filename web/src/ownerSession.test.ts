import { afterEach, expect, test, vi } from 'vitest';
import { createOwnerSession } from './ownerSession';

afterEach(() => vi.unstubAllGlobals());

test('owner requests carry their captured context with JSON and multipart bodies', async () => {
  const requests: RequestInit[] = [];
  vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => { requests.push(init!); return Response.json({ ok: true }); }));
  const session = createOwnerSession({}, vi.fn(), vi.fn(), 'first-session');
  await session.api('/api/models/m1', { method: 'PATCH', body: JSON.stringify({ title: 'Title' }) });
  await session.api('/api/models', { method: 'POST', body: new FormData() });
  for (const request of requests) {
    expect(new Headers(request.headers).get('X-Fileament-Context')).toBe('first-session');
    expect(request.credentials).toBe('include');
  }
  expect(new Headers(requests[0].headers).get('Content-Type')).toBe('application/json');
  expect(new Headers(requests[1].headers).has('Content-Type')).toBe(false);
  session.end();
});

test('ending a session aborts its request and prevents later chained writes', async () => {
  let finish: ((response: Response) => void) | undefined;
  let signal: AbortSignal | null | undefined;
  const fetch = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
    signal = init?.signal;
    return new Promise<Response>((resolve) => { finish = resolve; });
  });
  vi.stubGlobal('fetch', fetch);
  const ended = vi.fn();
  const session = createOwnerSession({}, ended, vi.fn());
  session.client.setQueryData(['model', 'm1'], { title: 'Private' });
  const pending = session.api('/api/models/m1');
  session.end();
  expect(signal?.aborted).toBe(true);
  expect(session.client.getQueryCache().getAll()).toHaveLength(0);
  finish?.(Response.json({ title: 'Late private data' }));
  await expect(pending).rejects.toMatchObject({ name: 'AbortError' });
  await expect(session.api('/api/models/m1', { method: 'DELETE' })).rejects.toMatchObject({ name: 'AbortError' });
  expect(fetch).toHaveBeenCalledTimes(1);
  expect(ended).toHaveBeenCalledTimes(1);
});

test('caller cancellation suppresses a late result without ending the session', async () => {
  const controller = new AbortController();
  let finish: ((response: Response) => void) | undefined;
  vi.stubGlobal('fetch', vi.fn().mockImplementationOnce(() => new Promise<Response>((resolve) => { finish = resolve; })).mockResolvedValue(Response.json({ title: 'Current' })));
  const ended = vi.fn();
  const session = createOwnerSession({}, ended, vi.fn());
  const pending = session.api('/api/models/m1', { signal: controller.signal });
  controller.abort();
  finish?.(Response.json({ error: 'late unauthorized response' }, { status: 401 }));
  await expect(pending).rejects.toMatchObject({ name: 'AbortError' });
  expect(await session.api('/api/models/m1')).toEqual({ title: 'Current' });
  expect(ended).not.toHaveBeenCalled();
  session.end();
});

test('a rejected password check revalidates authentication without treating it as logout', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(Response.json({ error: 'invalid current password' }, { status: 401 })));
  const ended = vi.fn();
  const refresh = vi.fn();
  const session = createOwnerSession({}, ended, refresh);
  await expect(session.api('/api/auth/password', { method: 'POST' })).rejects.toMatchObject({ status: 401 });
  expect(refresh).toHaveBeenCalledTimes(1);
  expect(ended).not.toHaveBeenCalled();
  session.end();
});

test('private unauthorized responses end the session, but a late response cannot end another one', async () => {
  let finish: ((response: Response) => void) | undefined;
  vi.stubGlobal('fetch', vi.fn().mockImplementationOnce(() => new Promise<Response>((resolve) => { finish = resolve; })).mockResolvedValue(Response.json({ error: 'authentication required' }, { status: 401 })));
  const ended = vi.fn();
  const session = createOwnerSession({}, ended, vi.fn());
  const pending = session.api('/api/models/m1');
  await expect(session.api('/api/models/m2')).rejects.toMatchObject({ status: 401 });
  expect(ended).toHaveBeenCalledTimes(1);
  finish?.(Response.json({ error: 'authentication required' }, { status: 401 }));
  await expect(pending).rejects.toMatchObject({ name: 'AbortError' });
  expect(ended).toHaveBeenCalledTimes(1);
});
