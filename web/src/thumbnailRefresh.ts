import { useQueryClient, type Query, type QueryClient } from '@tanstack/react-query';
import { useEffect } from 'react';

const BATCH_DELAY = 250;
const MAX_PENDING_MODELS = 256;

export function subscribeThumbnailRefresh(refresh: (models: Set<string> | null) => Promise<unknown> | void, intervalMs = 60_000, reconcileModels?: () => Iterable<string>) {
  const events = typeof EventSource === 'undefined' ? undefined : new EventSource('/api/events');
  const pending = new Set<string>();
  let reconcile = false;
  let running = false;
  let stopped = false;
  let timer: number | undefined;
  const schedule = () => {
    if (stopped || running || timer !== undefined || (!reconcile && pending.size === 0)) return;
    timer = window.setTimeout(() => {
      timer = undefined;
      const models = reconcile ? null : new Set(pending);
      reconcile = false;
      pending.clear();
      running = true;
      void Promise.resolve().then(() => { if (!stopped) return refresh(models); })
        .catch(() => { /* The next reconciliation retries failed refreshes. */ })
        .finally(() => { running = false; schedule(); });
    }, BATCH_DELAY);
  };
  const enqueue = (model: string) => {
    if (stopped) return;
    if (!reconcile) pending.add(model);
    if (pending.size > MAX_PENDING_MODELS) {
      reconcile = true;
      pending.clear();
    }
    schedule();
  };
  const reconcileAll = () => {
    if (stopped) return;
    if (reconcileModels) {
      for (const model of reconcileModels()) enqueue(model);
      return;
    }
    reconcile = true;
    pending.clear();
    schedule();
  };
  events?.addEventListener('thumbnail', (event) => {
    if (stopped) return;
    try {
      const data: unknown = JSON.parse((event as MessageEvent).data);
      if (!data || typeof data !== 'object' || !('modelId' in data) || typeof data.modelId !== 'string' || !data.modelId || data.modelId.length > 128) return;
      enqueue(data.modelId);
    } catch { /* Ignore incomplete events; periodic reconciliation covers them. */ }
  });
  events?.addEventListener('open', reconcileAll);
  const interval = window.setInterval(reconcileAll, intervalMs);
  return () => {
    stopped = true;
    pending.clear();
    window.clearTimeout(timer);
    window.clearInterval(interval);
    events?.close();
  };
}

export function refreshThumbnailQueries(client: QueryClient, models: Set<string> | null) {
  const predicate = (query: Query) => {
    const kind = query.queryKey[0];
    if (kind === 'model') return !models || models.has(String(query.queryKey[1]));
    if (kind === 'collection' && models) {
      const data = query.state.data as { pages: { models?: { id: string }[] }[] } | undefined;
      return !data || data.pages.some((page) => page.models?.some((model) => models.has(model.id)));
    }
    return kind === 'models' || kind === 'collections' || kind === 'collection';
  };
  const inflight = new Set(client.getQueryCache().findAll({ predicate }).filter((query) => query.isActive() && query.state.fetchStatus === 'fetching'));
  return client.invalidateQueries({ predicate }, { cancelRefetch: false }).then(() => {
    // A request started before the event can return an older snapshot.
    if (inflight.size) return client.invalidateQueries({ predicate: (query) => inflight.has(query) }, { cancelRefetch: false });
  });
}

export function useThumbnailQueryRefresh() {
  const client = useQueryClient();
  useEffect(() => subscribeThumbnailRefresh((models) => refreshThumbnailQueries(client, models)), [client]);
}
