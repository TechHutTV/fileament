import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen } from '@testing-library/react';
import '@testing-library/jest-dom/vitest';
import { expect, test, vi } from 'vitest';
import { App } from './App';

test('renders catalog shell when authenticated', async () => {
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url.includes('/api/me')) {
      return Response.json({ authenticated: true, setupRequired: false });
    }
    if (url.includes('/api/models')) {
      return Response.json({ items: [{ id: 'm1', title: 'Calibration Cube', description: '', totalBytes: 1024, files: [] }], nextCursor: '' });
    }
    return Response.json({});
  }));
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<QueryClientProvider client={client}><App /></QueryClientProvider>);
  expect(await screen.findByText('Calibration Cube')).toBeInTheDocument();
  expect(screen.getByRole('link', { name: /upload/i })).toBeInTheDocument();
});
