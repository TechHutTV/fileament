import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import '@testing-library/jest-dom/vitest';
import { afterEach, expect, test, vi } from 'vitest';
import { App } from './App';

vi.mock('./Viewer', () => ({ default: ({ file }: { file: { id: string } }) => {
  if (file.id === 'f1') throw new Error('Viewer failed');
  return <div>Working 3D view</div>;
} }));

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  localStorage.clear();
});

test.each(['getItem', 'setItem'] as const)('login and theme controls work when storage %s throws', async (operation) => {
  window.history.pushState({}, '', '/');
  vi.spyOn(console, 'error').mockImplementation(() => {});
  vi.spyOn(Storage.prototype, operation).mockImplementation(() => { throw new DOMException('Storage blocked', 'SecurityError'); });
  let authenticated = false;
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url === '/api/me') return Response.json({ authenticated });
    if (url === '/api/auth/login') { authenticated = true; return new Response(null, { status: 204 }); }
    if (url.startsWith('/api/models?')) return Response.json({ items: [], nextCursor: '' });
    return Response.json([]);
  }));
  renderApp();
  fireEvent.change(await screen.findByLabelText('Password'), { target: { value: 'password-password' } });
  fireEvent.click(screen.getByRole('button', { name: /log in/i }));
  fireEvent.click(await screen.findByRole('button', { name: 'Toggle dark mode' }));
  await waitFor(() => expect(document.documentElement.dataset.theme).toBe('dark'));
  expect(screen.getByRole('link', { name: 'Upload' })).toBeInTheDocument();
});

test.each(['owner', 'public'])('%s viewer failure preserves downloads and another variant can load', async (scope) => {
  window.history.pushState({}, '', scope === 'owner' ? '/models/m1' : '/s/token');
  vi.spyOn(console, 'error').mockImplementation(() => {});
  const file = { id: 'f1', modelId: 'm1', filename: 'broken.stl', relPath: 'files/broken.stl', format: 'stl', sizeBytes: 100, triangleCount: 1, bboxX: 1, bboxY: 1, bboxZ: 1 };
  const model = { id: 'm1', title: 'Model', description: '', totalBytes: 200, files: [file, { ...file, id: 'f2', filename: 'working.stl' }] };
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url === '/api/me') return Response.json({ authenticated: true });
    if (url === '/api/models/m1') return Response.json(model);
    if (url === '/api/public/token') return Response.json({ model, share: {} });
    if (url.endsWith('/status')) return new Response(null, { status: 204 });
    return Response.json([]);
  }));
  renderApp();
  expect(await screen.findByRole('alert')).toHaveTextContent('The 3D view could not be loaded. Downloads are still available.');
  expect(screen.getByRole('link', { name: /^broken.stl/ })).toHaveAttribute('href', scope === 'owner' ? '/files/m1/f1' : '/api/public/token/files/f1');
  if (scope === 'owner') {
    expect(screen.getByRole('link', { name: 'Upload' })).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: /Choose variant/ }));
    fireEvent.click(screen.getByRole('menuitemradio', { name: /working.stl/ }));
  } else {
    fireEvent.change(screen.getByLabelText('Variant'), { target: { value: 'f2' } });
  }
  expect(await screen.findByText('Working 3D view')).toBeInTheDocument();
  expect(screen.queryByRole('alert')).not.toBeInTheDocument();
});

function renderApp() {
  vi.stubGlobal('EventSource', class { addEventListener = vi.fn(); close = vi.fn(); });
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })}><App /></QueryClientProvider>);
}
