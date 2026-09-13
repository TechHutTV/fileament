import { QueryClient, type DefaultOptions } from '@tanstack/react-query';
import { createContext, useContext } from 'react';
import { api, APIError } from './api';

export function createOwnerSession(defaultOptions: DefaultOptions, onEnd: () => void, onRefresh: (message?: string) => void, context?: string) {
  const client = new QueryClient({ defaultOptions });
  const requests = new Set<AbortController>();
  let closed = false;
  let mounts = 0;
  const close = () => {
    closed = true;
    for (const request of requests) request.abort();
    requests.clear();
    void client.cancelQueries();
    client.clear();
  };
  const session = {
    client,
    mount() {
      mounts++;
      return () => {
        mounts--;
        // StrictMode immediately mounts the same effects again.
        queueMicrotask(() => { if (!mounts) close(); });
      };
    },
    end() { close(); onEnd(); },
    refresh: onRefresh,
    async api(path: string, init: RequestInit = {}) {
      if (closed) throw new DOMException('Owner session ended', 'AbortError');
      const request = new AbortController();
      const abort = () => request.abort();
      if (init.signal?.aborted) abort();
      else init.signal?.addEventListener('abort', abort, { once: true });
      requests.add(request);
      try {
        if (request.signal.aborted) throw new DOMException('Request canceled', 'AbortError');
        const headers = new Headers(init.headers);
        if (context) headers.set('X-Fileament-Context', context);
        const result = await api(path, { ...init, headers, signal: request.signal });
        if (closed || request.signal.aborted) throw new DOMException('Owner request canceled', 'AbortError');
        return result;
      } catch (error) {
        if (closed || request.signal.aborted) throw new DOMException('Owner request canceled', 'AbortError');
        if (error instanceof APIError && error.status === 401) {
          if (path.startsWith('/api/auth/')) onRefresh();
          else session.end();
        }
        throw error;
      } finally {
        requests.delete(request);
        init.signal?.removeEventListener('abort', abort);
      }
    },
  };
  return session;
}

export const OwnerSessionContext = createContext<ReturnType<typeof createOwnerSession> | null>(null);

export function useOwnerSession() {
  const session = useContext(OwnerSessionContext);
  if (!session) throw new Error('Owner session is required');
  return session;
}

export function useOwnerAPI() {
  return useOwnerSession().api;
}
