import { focusManager, QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, render, screen, waitFor } from '@testing-library/react';
import '@testing-library/jest-dom/vitest';
import { afterEach, expect, test, vi } from 'vitest';
import { App } from './App';

vi.mock('./Viewer', () => ({ default: ({ file }: { file: { id: string } }) => <div aria-label={`3D view ${file.id}`} /> }));
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); focusManager.setFocused(undefined); });
function renderPage(path: string) {
  window.history.replaceState({}, '', path);
  vi.stubGlobal('EventSource', class { addEventListener = vi.fn(); close = vi.fn(); });
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })}><App /></QueryClientProvider>);
}
async function refresh() { await act(async () => { focusManager.setFocused(false); focusManager.setFocused(true); }); }

test('catalog thumbnails load without IntersectionObserver and update with the selected cover', async () => {
  vi.stubGlobal('IntersectionObserver', undefined);
  let thumb = 'first.png';
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    if (String(input) === '/api/me') return Response.json({ authenticated: true });
    if (String(input).startsWith('/api/models?')) return Response.json({ items: [{ id: 'm1', title: 'Part', primaryThumb: thumb, totalBytes: 1, files: [] }], nextCursor: '' });
    return Response.json([]);
  }));
  renderPage('/');
  expect(await screen.findByAltText('Part thumbnail')).toHaveAttribute('src', '/thumbs/m1/first.png');
  expect(screen.getByAltText('Part thumbnail')).toHaveAttribute('loading', 'lazy');
  thumb = 'chosen.png'; await refresh();
  await waitFor(() => expect(screen.getByAltText('Part thumbnail')).toHaveAttribute('src', '/thumbs/m1/chosen.png'));
  expect(screen.getByRole('link', { name: /Part/ })).toHaveAttribute('href', '/models/m1');
});
