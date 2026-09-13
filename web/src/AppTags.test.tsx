import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import '@testing-library/jest-dom/vitest';
import { afterEach, expect, test, vi } from 'vitest';
import { App } from './App';

afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); });

test('non-ASCII tag labels filter using the server-provided slug and can be cleared', async () => {
  window.history.replaceState({}, '', '/');
  vi.stubGlobal('EventSource', class { addEventListener = vi.fn(); close = vi.fn(); });
  const part = { id: 'm1', title: 'Tagged part', totalBytes: 1, files: [] };
  const other = { ...part, id: 'm2', title: 'Other part' };
  const calls: string[] = [];
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const path = String(input); calls.push(path);
    if (path === '/api/me') return Response.json({ authenticated: true });
    if (path === '/api/tags') return Response.json([{ name: '模型', slug: 'u_example' }]);
    if (path.startsWith('/api/models?')) return Response.json({ items: new URL(path, 'http://localhost').searchParams.has('tag') ? [part] : [part, other], nextCursor: '' });
    return Response.json([]);
  }));
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><App /></QueryClientProvider>);
  await screen.findByRole('option', { name: '模型' });
  await screen.findByRole('link', { name: /Other part/ });
  fireEvent.change(screen.getByLabelText('Tag'), { target: { value: 'u_example' } });
  await waitFor(() => expect(screen.queryByRole('link', { name: /Other part/ })).not.toBeInTheDocument());
  expect(calls.some((path) => path.includes('tag=u_example'))).toBe(true);
  expect(await screen.findByRole('link', { name: /Tagged part/ })).toBeInTheDocument();
  fireEvent.click(screen.getByRole('button', { name: 'Clear' }));
  expect(await screen.findByRole('link', { name: /Other part/ })).toBeInTheDocument();
});
