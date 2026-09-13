import { useEffect, useRef, useState } from 'react';
import type { PublicPageData } from './App';
import { api, APIError } from './api';

type Loading = 'initial' | 'refresh' | 'more' | null;
const REFRESH_MS = 60_000;
const REQUEST_MS = 10_000;

export function usePublicShare(token: string, selected: string | null) {
  const [pages, setPages] = useState<PublicPageData[]>([]);
  const [loading, setLoading] = useState<Loading>('initial');
  const [error, setError] = useState(false);
  const [unavailable, setUnavailable] = useState(false);
  const actions = useRef({ refresh: () => {}, more: () => {} });
  useEffect(() => {
    let active = true;
    let ended = false;
    let current: PublicPageData[] = [];
    let visited = false;
    let lastRefresh = Date.now();
    let metadata: AbortController | undefined;
    let status: AbortController | undefined;
    let expiryTimer = 0;
    let metadataTimer = 0;
    let statusTimer = 0;
    const end = () => {
      ended = true;
      metadata?.abort();
      status?.abort();
      window.clearTimeout(metadataTimer);
      window.clearTimeout(statusTimer);
      current = [];
      setPages([]);
      setUnavailable(true);
    };
    const scheduleExpiry = (expiresAt?: number) => {
      window.clearTimeout(expiryTimer);
      if (!expiresAt) return;
      const check = () => {
        const remaining = expiresAt * 1000 - Date.now();
        if (remaining <= 0) end();
        else expiryTimer = window.setTimeout(check, Math.min(remaining, 2_147_483_647));
      };
      check();
    };
    const load = async (kind: Exclude<Loading, null>) => {
      if (!active || ended || metadata) return;
      let cursor = kind === 'more' ? current.at(-1)?.collection?.nextCursor : '';
      if (kind === 'more' && !cursor) return;
      const count = kind === 'refresh' ? Math.max(1, current.length) : 1;
      let next = kind === 'more' ? [...current] : [];
      const controller = new AbortController();
      metadata = controller;
      if (kind !== 'more') lastRefresh = Date.now();
      setLoading(kind);
      setError(false);
      try {
        for (let index = 0; index < count; index++) {
          const query = new URLSearchParams();
          if (selected) query.set('model', selected);
          if (cursor) query.set('cursor', cursor);
          if (visited || cursor) query.set('refresh', '1');
          metadataTimer = window.setTimeout(() => controller.abort(), REQUEST_MS);
          let page: PublicPageData;
          try {
            page = await api(`/api/public/${token}${query.size ? `?${query}` : ''}`, { signal: controller.signal });
          } finally {
            window.clearTimeout(metadataTimer);
          }
          if (!active || ended || controller.signal.aborted) return;
          visited = true;
          if (kind !== 'more' && index === 0) scheduleExpiry(page.share?.expiresAt);
          if (ended) return;
          next = [...next, page];
          // Publish fresh pages immediately so later failures cannot retain removed members.
          current = next;
          setPages(next);
          cursor = page.collection?.nextCursor;
          if (!cursor) break;
        }
      } catch (failure) {
        if (!active || ended) return;
        if (failure instanceof APIError && (failure.status === 404 || failure.status === 410)) end();
        else setError(true);
      } finally {
        metadata = undefined;
        if (active && !ended) setLoading(null);
      }
    };
    const check = async () => {
      if (!active || ended || status || document.visibilityState !== 'visible') return;
      const controller = new AbortController();
      status = controller;
      statusTimer = window.setTimeout(() => controller.abort(), REQUEST_MS);
      try {
        const response = await fetch(`/api/public/${token}/status`, { credentials: 'include', signal: controller.signal });
        if (!active || ended || controller.signal.aborted) return;
        if (response.status === 404 || response.status === 410) end();
        else if (response.ok && Date.now() - lastRefresh >= REFRESH_MS) void load('refresh');
      } catch {
        // A transient access-check failure leaves the current page available.
      } finally {
        window.clearTimeout(statusTimer);
        status = undefined;
      }
    };
    actions.current = { refresh: () => { void load('refresh'); }, more: () => { void load('more'); } };
    void load('initial');
    void check();
    const interval = window.setInterval(() => { void check(); }, REFRESH_MS);
    window.addEventListener('focus', check);
    window.addEventListener('online', check);
    document.addEventListener('visibilitychange', check);
    return () => {
      active = false;
      metadata?.abort();
      status?.abort();
      window.clearTimeout(metadataTimer);
      window.clearTimeout(statusTimer);
      window.clearInterval(interval);
      window.clearTimeout(expiryTimer);

      window.removeEventListener('focus', check);
      window.removeEventListener('online', check);
      document.removeEventListener('visibilitychange', check);
    };
  }, [token, selected]);
  return { pages, loading, error, unavailable, refresh: () => actions.current.refresh(), more: () => actions.current.more() };
}
