import { QueryClient, QueryObserver } from '@tanstack/react-query';
import { afterEach, expect, test, vi } from 'vitest';
import { refreshThumbnailQueries, subscribeThumbnailRefresh } from './thumbnailRefresh';

afterEach(() => { vi.useRealTimers(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

function stream() {
  vi.useFakeTimers();
  const listeners = new Map<string, (event: MessageEvent) => void>();
  const close = vi.fn();
  vi.stubGlobal('EventSource', class {
    addEventListener(name: string, listener: (event: MessageEvent) => void) { listeners.set(name, listener); }
    close = close;
  });
  return { close, emit: (modelId: string) => listeners.get('thumbnail')?.(new MessageEvent('thumbnail', { data: JSON.stringify({ modelId }) })), open: () => listeners.get('open')?.(new MessageEvent('open')) };
}

test('batches bursts without postponing updates during a continuous stream', async () => {
  const events = stream();
  const refresh = vi.fn();
  const stop = subscribeThumbnailRefresh(refresh);
  try {
    for (let i = 0; i < 5; i++) { events.emit('m'+i); await vi.advanceTimersByTimeAsync(50); }
    expect(refresh).toHaveBeenCalledTimes(1);
    expect(refresh).toHaveBeenLastCalledWith(new Set(['m0', 'm1', 'm2', 'm3', 'm4']));
    events.emit('m5');
    await vi.advanceTimersByTimeAsync(250);
    expect(refresh).toHaveBeenCalledTimes(2);
  } finally { stop(); }
});

test('keeps a trailing refresh for events received while the current request is running', async () => {
  const events = stream();
  let finish: () => void = () => undefined;
  const refresh = vi.fn().mockImplementationOnce(() => new Promise<void>((resolve) => { finish = resolve; }));
  const stop = subscribeThumbnailRefresh(refresh);
  try {
    events.emit('m1');
    await vi.advanceTimersByTimeAsync(250);
    for (let i = 0; i < 100; i++) events.emit('m1');
    await vi.advanceTimersByTimeAsync(1000);
    expect(refresh).toHaveBeenCalledTimes(1);
    finish();
    await vi.advanceTimersByTimeAsync(250);
    expect(refresh).toHaveBeenCalledTimes(2);
    expect(refresh).toHaveBeenLastCalledWith(new Set(['m1']));
  } finally { stop(); }
});

test('reconciles after reconnect, queue overflow, and a failed refresh', async () => {
  const events = stream();
  const refresh = vi.fn().mockRejectedValueOnce(new Error('offline'));
  const stop = subscribeThumbnailRefresh(refresh, 1000);
  try {
    events.open();
    await vi.advanceTimersByTimeAsync(250);
    expect(refresh).toHaveBeenLastCalledWith(null);
    await vi.advanceTimersByTimeAsync(1000);
    expect(refresh).toHaveBeenCalledTimes(2);
    for (let i = 0; i < 1000; i++) events.emit('m'+i);
    await vi.advanceTimersByTimeAsync(250);
    expect(refresh).toHaveBeenLastCalledWith(null);
    expect(refresh).toHaveBeenCalledTimes(3);
  } finally { stop(); }
});

test('stops queued and late work on unmount and reconciles when EventSource is unavailable', async () => {
  const events = stream();
  const refresh = vi.fn();
  const stop = subscribeThumbnailRefresh(refresh);
  events.emit('m1');
  stop();
  events.emit('m2');
  events.open();
  await vi.advanceTimersByTimeAsync(120_000);
  expect(refresh).not.toHaveBeenCalled();
  expect(events.close).toHaveBeenCalledTimes(1);
  vi.stubGlobal('EventSource', undefined);
  const stopFallback = subscribeThumbnailRefresh(refresh);
  try {
    await vi.advanceTimersByTimeAsync(60_250);
    expect(refresh).toHaveBeenCalledExactlyOnceWith(null);
  } finally { stopFallback(); }
});

test('refreshes only affected owner model and collection details and leaves public data alone', async () => {
  const client = new QueryClient();
  try {
    client.setQueryData(['model', 'm1'], { primaryThumb: 'selected.png' });
    client.setQueryData(['model', 'm2'], { primaryThumb: 'other.png' });
    client.setQueryData(['collection', 'first'], { pages: [{ models: [{ id: 'm1' }] }] });
    client.setQueryData(['collection', 'second'], { pages: [{ models: [{ id: 'm2' }] }] });
    client.setQueryData(['models', ''], { pages: [] });
    client.setQueryData(['collections'], []);
    client.setQueryData(['public', 'token'], {});
    await refreshThumbnailQueries(client, new Set(['m1']));
    for (const key of [['model', 'm1'], ['collection', 'first'], ['models', ''], ['collections']]) expect(client.getQueryState(key)?.isInvalidated).toBe(true);
    for (const key of [['model', 'm2'], ['collection', 'second'], ['public', 'token']]) expect(client.getQueryState(key)?.isInvalidated).toBe(false);
    expect(client.getQueryData(['model', 'm1'])).toEqual({ primaryThumb: 'selected.png' });
  } finally { client.clear(); }
});

test('filtered reconciliation retains queued ready-model events and stops polling completed uploads', async () => {
  const events = stream();
  let pending: string[] = ['processing'];
  const refresh = vi.fn();
  const stop = subscribeThumbnailRefresh(refresh, 1000, () => pending);
  try {
    events.emit('ready');
    events.open();
    await vi.advanceTimersByTimeAsync(250);
    expect(refresh).toHaveBeenCalledExactlyOnceWith(new Set(['ready', 'processing']));
    pending = [];
    await vi.advanceTimersByTimeAsync(2000);
    expect(refresh).toHaveBeenCalledTimes(1);
    for (let i = 0; i < 1000; i++) events.emit('ready-'+i);
    await vi.advanceTimersByTimeAsync(250);
    expect(refresh).toHaveBeenLastCalledWith(null);
  } finally { stop(); }
});

test('reconciles again after a request that began before the thumbnail event', async () => {
  const client = new QueryClient();
  const responses: ((model: { primaryThumb: string }) => void)[] = [];
  const observer = new QueryObserver(client, { queryKey: ['model', 'm1'], queryFn: () => new Promise<{ primaryThumb: string }>((resolve) => responses.push(resolve)) });
  const unsubscribe = observer.subscribe(() => undefined);
  try {
    expect(responses).toHaveLength(1);
    const refreshed = refreshThumbnailQueries(client, new Set(['m1']));
    responses[0]({ primaryThumb: '' });
    await vi.waitFor(() => expect(responses).toHaveLength(2));
    responses[1]({ primaryThumb: 'selected.png' });
    await refreshed;
    expect(observer.getCurrentResult().data).toEqual({ primaryThumb: 'selected.png' });
  } finally { unsubscribe(); client.clear(); }
});
