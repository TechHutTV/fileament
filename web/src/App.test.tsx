import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import '@testing-library/jest-dom/vitest';
import { afterEach, beforeEach, expect, test, vi } from 'vitest';
import { App } from './App';

const model = {
	id: 'm1',
	title: 'Calibration Cube',
	description: '[Documentation](https://example.com)',
  totalBytes: 1024,
  files: [{ id: 'f1', modelId: 'm1', filename: 'cube.stl', relPath: 'files/cube.stl', format: 'stl', sizeBytes: 60 * 1024 * 1024, triangleCount: 12, bboxX: 1, bboxY: 1, bboxZ: 1 }],
  images: [],
  tags: ['tools'],
};

function stubVariantDetail() {
  const firstFile = { ...model.files[0], thumbPath: 'thumbs/f1.png' };
  const secondFile = { ...model.files[0], id: 'f2', filename: 'bracket.3mf', relPath: 'files/bracket.3mf', format: '3mf', thumbPath: 'thumbs/f2.png' };
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url.includes('/api/me')) return Response.json({ authenticated: true, setupRequired: false });
    if (url.includes('/api/models/m1')) return Response.json({ ...model, files: [firstFile, secondFile] });
    if (url.includes('/api/collections') || url.includes('/api/shares')) return Response.json([]);
    return Response.json({});
  }));
}

beforeEach(() => {
  window.history.pushState({}, '', '/');
  vi.stubGlobal('EventSource', class {
    addEventListener = vi.fn();
    close = vi.fn();
  });
});

afterEach(() => {
  vi.restoreAllMocks();
});

test('accepts an empty setup success response and transitions to login', async () => {
  const calls: string[] = [];
  let setupComplete = false;
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push(`${init?.method ?? 'GET'} ${String(input)}`);
    if (String(input).includes('/api/me')) return Response.json({ authenticated: false, setupRequired: !setupComplete });
    if (String(input).includes('/api/auth/setup')) {
      setupComplete = true;
      return new Response(null, { status: 201 });
    }
    return Response.json({});
  }));
  renderApp();
  expect(await screen.findByText('Set owner password')).toBeInTheDocument();
  fireEvent.change(screen.getByLabelText('Password'), { target: { value: 'correct horse password' } });
  fireEvent.click(screen.getByRole('button', { name: /show password/i }));
  expect(screen.getByLabelText('Password')).toHaveAttribute('type', 'text');
  fireEvent.click(screen.getByRole('button', { name: /create owner/i }));
  await waitFor(() => expect(calls).toContain('POST /api/auth/setup'));
  expect(await screen.findByText('Owner login')).toBeInTheDocument();
  expect(screen.getByText('Your 3D files, organized.')).toBeInTheDocument();
  expect(screen.getByLabelText('Password')).toHaveValue('');
  expect(screen.getByLabelText('Password')).toHaveAttribute('type', 'password');
  expect(screen.queryByText('Authentication failed')).not.toBeInTheDocument();
});

test('manages a dropped multi-model upload queue', async () => {
  window.history.pushState({}, '', '/upload');
  const uploads: string[] = [];
  const calls: string[] = [];
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push(`${init?.method ?? 'GET'} ${url}`);
    if (url.includes('/api/me')) return Response.json({ authenticated: true, setupRequired: false });
    if (url === '/api/models' && init?.method === 'POST') {
      const file = (init.body as FormData).get('file') as File;
      uploads.push(file.name);
      return Response.json({
        ...model,
        id: file.name,
        title: file.name,
        totalBytes: file.size,
        primaryThumb: 'card.png',
        files: [{ ...model.files[0], id: `${file.name}-file`, modelId: file.name, filename: file.name, sizeBytes: file.size }],
      }, { status: 201 });
    }
    if (url.startsWith('/api/models/') && init?.method === 'DELETE') return new Response(null, { status: 204 });
    if (url.includes('/api/models?')) return Response.json({ items: [], nextCursor: '' });
    if (url.includes('/api/collections') || url.includes('/api/tags')) return Response.json([]);
    return Response.json({});
  }));
  renderApp();

  await chooseSeparateModels();
  const dropzone = await screen.findByRole('button', { name: /drop 3d files/i });
  expect(screen.getByLabelText(/choose 3d files/i)).toHaveAttribute('multiple');
  const cube = new File(['solid cube'], 'cube.stl', { type: 'model/stl' });
  const bracket = new File(['solid bracket'], 'bracket.stl', { type: 'model/stl' });
  fireEvent.drop(dropzone, { dataTransfer: { files: [cube, bracket] } });

  await waitFor(() => expect(uploads).toEqual(['cube.stl', 'bracket.stl']));
  expect(await screen.findByAltText('cube.stl thumbnail')).toHaveAttribute('src', '/thumbs/cube.stl/card.png');
  expect(screen.getByAltText('bracket.stl thumbnail')).toHaveAttribute('src', '/thumbs/bracket.stl/card.png');

  fireEvent.click(screen.getByRole('button', { name: 'Remove cube.stl' }));
  const dialog = await screen.findByRole('alertdialog', { name: 'Delete uploaded model?' });
  expect(within(dialog).getByText(/cube\.stl.*library/)).toBeInTheDocument();
  expect(calls).not.toContain('DELETE /api/models/cube.stl');
  fireEvent.click(within(dialog).getByRole('button', { name: 'Delete model' }));
  await waitFor(() => expect(calls).toContain('DELETE /api/models/cube.stl'));
  expect(screen.queryByText('cube.stl')).not.toBeInTheDocument();

  fireEvent.click(screen.getByRole('button', { name: /finish and view library/i }));
  expect(window.location.pathname).toBe('/');
});

test('runs up to three dropped model uploads concurrently', async () => {
  window.history.pushState({}, '', '/upload');
  const started: string[] = [];
  const finish = new Map<string, () => void>();
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url.includes('/api/me')) return Response.json({ authenticated: true, setupRequired: false });
    if (url === '/api/models' && init?.method === 'POST') {
      const file = (init.body as FormData).get('file') as File;
      started.push(file.name);
      return new Promise<Response>((resolve) => {
        finish.set(file.name, () => resolve(Response.json({
          ...model,
          id: file.name,
          title: file.name,
          primaryThumb: 'card.png',
          files: [{ ...model.files[0], id: `${file.name}-file`, modelId: file.name, filename: file.name }],
        }, { status: 201 })));
      });
    }
    if (url.includes('/api/collections') || url.includes('/api/tags')) return Response.json([]);
    return Response.json({});
  }));
  renderApp();

  await chooseSeparateModels();
  const files = ['one.stl', 'two.stl', 'three.stl', 'four.stl'].map((name) => new File([name], name, { type: 'model/stl' }));
  fireEvent.drop(screen.getByRole('button', { name: /drop 3d files/i }), { dataTransfer: { files } });

  await waitFor(() => expect(started).toEqual(['one.stl', 'two.stl', 'three.stl']));
  expect(started).not.toContain('four.stl');
  await act(async () => { finish.get('one.stl')?.(); });
  await waitFor(() => expect(started).toEqual(['one.stl', 'two.stl', 'three.stl', 'four.stl']));
  await act(async () => {
    finish.get('two.stl')?.();
    finish.get('three.stl')?.();
    finish.get('four.stl')?.();
  });
  expect(await screen.findByAltText('four.stl thumbnail')).toBeInTheDocument();
});

test('requires loose file organization before files can be selected', async () => {
  window.history.pushState({}, '', '/upload');
  const uploads: { path: string; files: string[] }[] = [];
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url.includes('/api/me')) return Response.json({ authenticated: true, setupRequired: false });
    if (url === '/api/collections') return Response.json([]);
    if ((url === '/api/models' || url === '/api/models/grouped') && init?.method === 'POST') {
      const body = init.body as FormData;
      const files = url.endsWith('/grouped')
        ? body.getAll('files').map((entry) => (entry as File).name)
        : [((body.get('file')) as File).name];
      uploads.push({ path: url, files });
      return Response.json({ ...model, id: 'grouped-model', title: 'Grouped model', primaryThumb: 'card.png' }, { status: 201 });
    }
    return Response.json({});
  }));
  renderApp();

  const separate = await screen.findByRole('radio', { name: /separate models/i });
  const grouped = screen.getByRole('radio', { name: /one model with variants/i });
  const input = screen.getByLabelText(/choose 3d files/i);
  const dropzone = screen.getByRole('button', { name: /choose file organization before adding files/i });
  expect(separate).not.toBeChecked();
  expect(grouped).not.toBeChecked();
  expect(input).toBeDisabled();
  expect(dropzone).toHaveAttribute('aria-disabled', 'true');

  const files = [new File(['a'], 'baseplate-2x2.stl'), new File(['b'], 'baseplate-3x3.stl')];
  fireEvent.drop(dropzone, { dataTransfer: { files } });
  expect(uploads).toEqual([]);

  fireEvent.click(grouped);
  expect(input).toBeEnabled();
  expect(dropzone).toHaveAttribute('aria-disabled', 'false');
  fireEvent.drop(dropzone, { dataTransfer: { files } });

  await waitFor(() => expect(uploads).toEqual([{ path: '/api/models/grouped', files: ['baseplate-2x2.stl', 'baseplate-3x3.stl'] }]));
});

test('creates one atomic model when loose files are grouped as variants', async () => {
  window.history.pushState({}, '', '/upload');
  const groupedUploads: { title: string; files: string[] }[] = [];
  const singleUploads: string[] = [];
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url.includes('/api/me')) return Response.json({ authenticated: true, setupRequired: false });
    if (url === '/api/collections') return Response.json([]);
    if (url === '/api/models/grouped' && init?.method === 'POST') {
      const body = init.body as FormData;
      const files = body.getAll('files').map((entry) => (entry as File).name);
      groupedUploads.push({ title: String(body.get('title') ?? ''), files });
      return Response.json({
        ...model,
        id: 'opengrid-baseplate',
        title: 'OpenGrid Baseplate',
        primaryThumb: 'card.png',
        files: files.map((filename, index) => ({ ...model.files[0], id: `f${index}`, modelId: 'opengrid-baseplate', filename })),
      }, { status: 201 });
    }
    if (url === '/api/models' && init?.method === 'POST') {
      singleUploads.push(((init.body as FormData).get('file') as File).name);
    }
    return Response.json({});
  }));
  renderApp();

  fireEvent.click(await screen.findByRole('radio', { name: /one model with variants/i }));
  fireEvent.change(screen.getByLabelText('Grouped model name'), { target: { value: 'OpenGrid Baseplate' } });
  fireEvent.drop(screen.getByRole('button', { name: /drop 3d files/i }), { dataTransfer: { files: [new File(['a'], 'baseplate-2x2.stl'), new File(['b'], 'baseplate-3x3.stl')] } });

  await waitFor(() => expect(groupedUploads).toEqual([{ title: 'OpenGrid Baseplate', files: ['baseplate-2x2.stl', 'baseplate-3x3.stl'] }]));
  expect(singleUploads).toEqual([]);
  expect(await screen.findByText('OpenGrid Baseplate')).toBeInTheDocument();
  const pendingPreview = within(screen.getByRole('region', { name: 'OpenGrid Baseplate variants' })).getByLabelText('baseplate-2x2.stl preview rendering');
  expect(pendingPreview).toHaveClass('loading');
  expect(within(pendingPreview).getByText('Preview pending')).toHaveClass('visually-hidden');
});

test('previews and removes an individual variant from a grouped upload', async () => {
  window.history.pushState({}, '', '/upload');
  const calls: string[] = [];
  const files = [new File(['variant-a'], 'baseplate-2x2.stl'), new File(['variant-b'], 'baseplate-3x3.stl')];
  let finishUpload: (response: Response) => void = () => undefined;
  const upload = new Promise<Response>((resolve) => { finishUpload = resolve; });
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push(`${init?.method ?? 'GET'} ${url}`);
    if (url.includes('/api/me')) return Response.json({ authenticated: true, setupRequired: false });
    if (url === '/api/collections') return Response.json([]);
    if (url === '/api/models/grouped' && init?.method === 'POST') {
      return upload;
    }
    if (url === '/api/models/opengrid-baseplate/files/f2' && init?.method === 'DELETE') return Response.json({
      ...model,
      id: 'opengrid-baseplate',
      title: 'OpenGrid Baseplate',
      primaryThumb: '',
      totalBytes: files[0].size,
      files: [{ ...model.files[0], id: 'f1', modelId: 'opengrid-baseplate', filename: files[0].name, sizeBytes: files[0].size, thumbPath: 'thumbs/f1.png' }],
    });
    return Response.json({});
  }));
  renderApp();

  fireEvent.click(await screen.findByRole('radio', { name: /one model with variants/i }));
  fireEvent.drop(screen.getByRole('button', { name: /drop 3d files/i }), { dataTransfer: { files } });

  const pendingVariants = await screen.findByRole('region', { name: 'baseplate-2x2.stl variants' });
  expect(within(pendingVariants).getByText('baseplate-2x2.stl')).toBeInTheDocument();
  expect(within(pendingVariants).getByText('baseplate-3x3.stl')).toBeInTheDocument();
  expect(within(pendingVariants).queryByRole('button', { name: /remove .* variant/i })).not.toBeInTheDocument();

  await act(async () => {
    finishUpload(Response.json({
      ...model,
      id: 'opengrid-baseplate',
      title: 'OpenGrid Baseplate',
      primaryThumb: 'f1.png',
      totalBytes: files.reduce((total, file) => total + file.size, 0),
      files: files.map((file, index) => ({
        ...model.files[0],
        id: `f${index + 1}`,
        modelId: 'opengrid-baseplate',
        filename: file.name,
        relPath: `files/${file.name}`,
        sizeBytes: file.size,
        thumbPath: `thumbs/f${index + 1}.png`,
      })),
    }, { status: 201 }));
  });

  const variants = await screen.findByRole('region', { name: 'OpenGrid Baseplate variants' });
  expect(within(variants).getByAltText('baseplate-2x2.stl variant preview')).toHaveAttribute('src', '/thumbs/opengrid-baseplate/f1.png');
  expect(within(variants).getByAltText('baseplate-3x3.stl variant preview')).toHaveAttribute('src', '/thumbs/opengrid-baseplate/f2.png');
  fireEvent.click(within(variants).getByRole('button', { name: 'Remove baseplate-3x3.stl variant' }));
  expect(calls).not.toContain('DELETE /api/models/opengrid-baseplate/files/f2');
  const dialog = await screen.findByRole('alertdialog', { name: 'Delete variant?' });
  expect(within(dialog).getByText(/baseplate-3x3\.stl/)).toBeInTheDocument();
  fireEvent.click(within(dialog).getByRole('button', { name: 'Delete variant' }));

  await waitFor(() => expect(calls).toContain('DELETE /api/models/opengrid-baseplate/files/f2'));
  await waitFor(() => expect(screen.queryByText('baseplate-3x3.stl')).not.toBeInTheDocument());
  expect(screen.getByText('baseplate-2x2.stl')).toBeInTheDocument();
  expect(screen.getByAltText('OpenGrid Baseplate thumbnail')).toHaveAttribute('src', '/thumbs/opengrid-baseplate/f1.png');
  expect(screen.queryByRole('button', { name: 'Remove baseplate-2x2.stl variant' })).not.toBeInTheDocument();
});

test('keeps ZIP bundles separate while grouping loose files', async () => {
  window.history.pushState({}, '', '/upload');
  const uploads: { path: string; files: string[] }[] = [];
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url.includes('/api/me')) return Response.json({ authenticated: true, setupRequired: false });
    if (url === '/api/collections') return Response.json([]);
    if ((url === '/api/models' || url === '/api/models/grouped') && init?.method === 'POST') {
      const body = init.body as FormData;
      const files = url.endsWith('/grouped')
        ? body.getAll('files').map((entry) => (entry as File).name)
        : [((body.get('file')) as File).name];
      uploads.push({ path: url, files });
      const name = files[0];
      return Response.json({ ...model, id: name, title: name, primaryThumb: 'card.png' }, { status: 201 });
    }
    return Response.json({});
  }));
  renderApp();

  fireEvent.click(await screen.findByRole('radio', { name: /one model with variants/i }));
  fireEvent.drop(screen.getByRole('button', { name: /drop 3d files/i }), { dataTransfer: { files: [
    new File(['zip-a'], 'alpha.zip'),
    new File(['mesh-a'], 'baseplate-2x2.stl'),
    new File(['mesh-b'], 'baseplate-3x3.stl'),
    new File(['zip-b'], 'beta.zip'),
  ] } });

  await waitFor(() => expect(uploads).toEqual([
    { path: '/api/models', files: ['alpha.zip'] },
    { path: '/api/models/grouped', files: ['baseplate-2x2.stl', 'baseplate-3x3.stl'] },
    { path: '/api/models', files: ['beta.zip'] },
  ]));
});

test('adds concurrent uploads to the selected collection in drop order', async () => {
  window.history.pushState({}, '', '/upload');
  const assignments: string[] = [];
  const finish = new Map<string, () => void>();
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url.includes('/api/me')) return Response.json({ authenticated: true, setupRequired: false });
    if (url === '/api/collections') return Response.json([{ id: 'c1', name: 'Fixtures', slug: 'fixtures', description: '' }]);
    if (url === '/api/models' && init?.method === 'POST') {
      const file = (init.body as FormData).get('file') as File;
      return new Promise<Response>((resolve) => {
        finish.set(file.name, () => resolve(Response.json({ ...model, id: file.name, title: file.name, primaryThumb: 'card.png' }, { status: 201 })));
      });
    }
    if (url.startsWith('/api/collections/c1/models/') && init?.method === 'PUT') {
      assignments.push(url.split('/').at(-1) ?? '');
      return new Response(null, { status: 204 });
    }
    return Response.json({});
  }));
  renderApp();

  const collection = await screen.findByLabelText('Add uploads to collection');
  await screen.findByRole('option', { name: 'Fixtures' });
  fireEvent.change(collection, { target: { value: 'c1' } });
  expect(collection).toHaveValue('c1');
  await chooseSeparateModels();
  fireEvent.drop(screen.getByRole('button', { name: /drop 3d files/i }), { dataTransfer: { files: [new File(['a'], 'cube.stl'), new File(['b'], 'bracket.stl')] } });

  await waitFor(() => expect(finish.size).toBe(2));
  await act(async () => { finish.get('bracket.stl')?.(); });
  expect(assignments).toEqual([]);
  await act(async () => { finish.get('cube.stl')?.(); });
  await waitFor(() => expect(assignments).toEqual(['cube.stl', 'bracket.stl']));
});

test('refreshes collections after removing an assigned upload', async () => {
  window.history.pushState({}, '', '/upload');
  let collectionFetches = 0;
  const calls: string[] = [];
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push(`${init?.method ?? 'GET'} ${url}`);
    if (url.includes('/api/me')) return Response.json({ authenticated: true, setupRequired: false });
    if (url === '/api/collections') {
      collectionFetches += 1;
      return Response.json([{ id: 'c1', name: 'Fixtures', slug: 'fixtures', description: '' }]);
    }
    if (url === '/api/models' && init?.method === 'POST') return Response.json({ ...model, id: 'cube.stl', title: 'cube.stl', primaryThumb: 'card.png' }, { status: 201 });
    if (url === '/api/collections/c1/models/cube.stl' && init?.method === 'PUT') return new Response(null, { status: 204 });
    if (url === '/api/models/cube.stl' && init?.method === 'DELETE') return new Response(null, { status: 204 });
    return Response.json({});
  }));
  renderApp();

  const collection = await screen.findByLabelText('Add uploads to collection');
  await screen.findByRole('option', { name: 'Fixtures' });
  fireEvent.change(collection, { target: { value: 'c1' } });
  await chooseSeparateModels();
  fireEvent.drop(screen.getByRole('button', { name: /drop 3d files/i }), { dataTransfer: { files: [new File(['a'], 'cube.stl')] } });
  await waitFor(() => expect(calls).toContain('PUT /api/collections/c1/models/cube.stl'));
  await waitFor(() => expect(collectionFetches).toBe(2));

  fireEvent.click(await screen.findByRole('button', { name: 'Remove cube.stl' }));
  const dialog = await screen.findByRole('alertdialog', { name: 'Delete uploaded model?' });
  fireEvent.click(within(dialog).getByRole('button', { name: 'Delete model' }));
  await waitFor(() => expect(calls).toContain('DELETE /api/models/cube.stl'));
  await waitFor(() => expect(collectionFetches).toBe(3));
});

test('cleans up a model cancelled while collection assignment is in flight', async () => {
  window.history.pushState({}, '', '/upload');
  let releaseAssignment: (response: Response) => void = () => undefined;
  const assignment = new Promise<Response>((resolve) => { releaseAssignment = resolve; });
  const calls: string[] = [];
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push(`${init?.method ?? 'GET'} ${url}`);
    if (url.includes('/api/me')) return Response.json({ authenticated: true, setupRequired: false });
    if (url === '/api/collections') return Response.json([{ id: 'c1', name: 'Fixtures', slug: 'fixtures', description: '' }]);
    if (url === '/api/models' && init?.method === 'POST') return Response.json({ ...model, id: 'cube.stl', title: 'cube.stl', primaryThumb: 'card.png' }, { status: 201 });
    if (url === '/api/collections/c1/models/cube.stl' && init?.method === 'PUT') return assignment;
    if (url === '/api/models/cube.stl' && init?.method === 'DELETE') return new Response(null, { status: 204 });
    return Response.json({});
  }));
  renderApp();

  const collection = await screen.findByLabelText('Add uploads to collection');
  await screen.findByRole('option', { name: 'Fixtures' });
  fireEvent.change(collection, { target: { value: 'c1' } });
  await chooseSeparateModels();
  fireEvent.drop(screen.getByRole('button', { name: /drop 3d files/i }), { dataTransfer: { files: [new File(['a'], 'cube.stl')] } });
  await waitFor(() => expect(calls).toContain('PUT /api/collections/c1/models/cube.stl'));

  fireEvent.click(screen.getByRole('button', { name: 'Cancel cube.stl upload' }));
  releaseAssignment(new Response(null, { status: 204 }));

  await waitFor(() => expect(calls).toContain('DELETE /api/models/cube.stl'));
  await waitFor(() => expect(calls.filter((call) => call === 'GET /api/collections')).toHaveLength(2));
  expect(calls.lastIndexOf('GET /api/collections')).toBeGreaterThan(calls.indexOf('DELETE /api/models/cube.stl'));
  expect(screen.queryByText('cube.stl')).not.toBeInTheDocument();
});

test('reconciles a thumbnail event that arrives before the upload response', async () => {
  window.history.pushState({}, '', '/upload');
  let thumbnail: EventListener = () => undefined;
  vi.stubGlobal('EventSource', class {
    addEventListener(event: string, listener: EventListener) {
      if (event === 'thumbnail') thumbnail = listener;
    }
    close = vi.fn();
  });
  let releaseUpload: (response: Response) => void = () => undefined;
  const uploadResponse = new Promise<Response>((resolve) => { releaseUpload = resolve; });
  const calls: string[] = [];
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push(`${init?.method ?? 'GET'} ${url}`);
    if (url.includes('/api/me')) return Response.json({ authenticated: true, setupRequired: false });
    if (url === '/api/models' && init?.method === 'POST') return uploadResponse;
    if (url === '/api/models/race.stl') return Response.json({ ...model, id: 'race.stl', title: 'race.stl', primaryThumb: 'card.png' });
    return Response.json({});
  }));
  renderApp();

  await chooseSeparateModels();
  const dropzone = await screen.findByRole('button', { name: /drop 3d files/i });
  fireEvent.drop(dropzone, { dataTransfer: { files: [new File(['race'], 'race.stl')] } });
  await screen.findByText('Uploading');
  thumbnail(new MessageEvent('thumbnail', { data: JSON.stringify({ modelId: 'race.stl' }) }));
  await waitFor(() => expect(calls).toContain('GET /api/models/race.stl'));
  releaseUpload(Response.json({ ...model, id: 'race.stl', title: 'race.stl', primaryThumb: undefined }, { status: 201 }));

  expect(await screen.findByAltText('race.stl thumbnail')).toHaveAttribute('src', '/thumbs/race.stl/card.png');
  expect(calls.filter((call) => call === 'GET /api/models/race.stl')).toHaveLength(2);
});

test('keeps a cancelled upload visible when server cleanup fails', async () => {
  window.history.pushState({}, '', '/upload');
  let releaseUpload: (response: Response) => void = () => undefined;
  const uploadResponse = new Promise<Response>((resolve) => { releaseUpload = resolve; });
  const uploadState = {
    signal: undefined as AbortSignal | undefined,
    wasAborted() { return this.signal?.aborted ?? false; },
  };
  const calls: string[] = [];
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push(`${init?.method ?? 'GET'} ${url}`);
    if (url.includes('/api/me')) return Response.json({ authenticated: true, setupRequired: false });
    if (url === '/api/models' && init?.method === 'POST') {
      uploadState.signal = init.signal as AbortSignal | undefined;
      return uploadResponse;
    }
    if (url === '/api/models/delayed.stl' && init?.method === 'DELETE') return new Response('cleanup failed', { status: 500 });
    return Response.json({});
  }));
  renderApp();

  await chooseSeparateModels();
  const dropzone = await screen.findByRole('button', { name: /drop 3d files/i });
  fireEvent.drop(dropzone, { dataTransfer: { files: [new File(['solid delayed'], 'delayed.stl')] } });
  fireEvent.click(await screen.findByRole('button', { name: 'Cancel delayed.stl upload' }));
  expect(uploadState.wasAborted()).toBe(false);
  releaseUpload(Response.json({ ...model, id: 'delayed.stl', title: 'delayed.stl' }, { status: 201 }));

  await waitFor(() => expect(calls).toContain('DELETE /api/models/delayed.stl'));
  expect(await screen.findByText('Upload completed, but could not remove the model.')).toBeInTheDocument();
  expect(screen.getByText('delayed.stl')).toBeInTheDocument();
});

test('cleans up active uploads and skips queued uploads after unmount', async () => {
  window.history.pushState({}, '', '/upload');
  const finish = new Map<string, () => void>();
  const uploads: string[] = [];
  const deleted: string[] = [];
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url.includes('/api/me')) return Response.json({ authenticated: true, setupRequired: false });
    if (url === '/api/models' && init?.method === 'POST') {
      const file = (init.body as FormData).get('file') as File;
      uploads.push(file.name);
      return new Promise<Response>((resolve) => {
        finish.set(file.name, () => resolve(Response.json({ ...model, id: file.name, title: file.name }, { status: 201 })));
      });
    }
    if (url.startsWith('/api/models/') && init?.method === 'DELETE') {
      deleted.push(url);
      return new Response(null, { status: 204 });
    }
    return Response.json({});
  }));
  const view = renderApp();

  await chooseSeparateModels();
  const dropzone = await screen.findByRole('button', { name: /drop 3d files/i });
  const files = ['active-a.stl', 'active-b.stl', 'active-c.stl', 'queued.stl'].map((name) => new File([name], name));
  fireEvent.drop(dropzone, { dataTransfer: { files } });
  await waitFor(() => expect(uploads).toEqual(['active-a.stl', 'active-b.stl', 'active-c.stl']));
  view.unmount();
  finish.get('active-a.stl')?.();
  finish.get('active-b.stl')?.();
  finish.get('active-c.stl')?.();

  await waitFor(() => expect(deleted.sort()).toEqual([
    '/api/models/active-a.stl',
    '/api/models/active-b.stl',
    '/api/models/active-c.stl',
  ]));
  expect(uploads).toEqual(['active-a.stl', 'active-b.stl', 'active-c.stl']);
});

test('catalog exposes filters, sorting, pagination, and owner nav', async () => {
  const urls: string[] = [];
  let refreshed = false;
  let thumbnail: EventListener = () => undefined;
  vi.stubGlobal('EventSource', class {
    addEventListener(event: string, listener: EventListener) {
      if (event === 'thumbnail') thumbnail = listener;
    }
    close = vi.fn();
  });
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    urls.push(url);
    if (url.includes('/api/me')) return Response.json({ authenticated: true, setupRequired: false });
    if (url.includes('/api/tags')) return Response.json([{ name: 'Tools', slug: 'tools' }]);
    if (url.includes('/api/collections')) return Response.json([{ id: 'c1', name: 'Fixtures', slug: 'fixtures', description: '' }]);
    if (url.includes('/api/models')) {
      const nextModel = { ...model, id: 'm2', title: 'Second Model', files: [{ ...model.files[0], id: 'f2', modelId: 'm2' }] };
      if (url.includes('cursor=')) return Response.json({ items: refreshed ? [] : [nextModel], nextCursor: '' });
      return Response.json({ items: [{ ...model, title: refreshed ? 'Calibration Cube Updated' : model.title }], nextCursor: 'next-page' });
    }
    return Response.json({});
  }));
  renderApp();
  expect(await screen.findByText('Calibration Cube')).toBeInTheDocument();
  expect(screen.getByRole('heading', { name: 'Models' })).toBeInTheDocument();
  expect(screen.getByText('1 model shown')).toBeInTheDocument();
  expect(screen.getByText('STL')).toBeInTheDocument();
  expect(screen.getByText(/12 tris/i)).toBeInTheDocument();
  expect(screen.getByRole('link', { name: /upload/i })).toBeInTheDocument();
  fireEvent.change(screen.getByLabelText('Search models'), { target: { value: 'cube' } });
  fireEvent.change(screen.getByLabelText('Tag'), { target: { value: 'tools' } });
  fireEvent.change(screen.getByLabelText('Collection'), { target: { value: 'fixtures' } });
  fireEvent.change(screen.getByLabelText('Sort'), { target: { value: 'title' } });
  await waitFor(() => expect(urls.some((u) => u.includes('q=cube') && u.includes('tag=tools') && u.includes('collection=fixtures') && u.includes('sort=title'))).toBe(true));
  fireEvent.click(screen.getByRole('button', { name: /load more/i }));
  expect(await screen.findByText('Second Model')).toBeInTheDocument();
  refreshed = true;
  thumbnail(new Event('thumbnail'));
  expect(await screen.findByText('Calibration Cube Updated')).toBeInTheDocument();
  await waitFor(() => expect(screen.queryByRole('link', { name: /second model/i })).not.toBeInTheDocument());
});

test('links to the source repository from the owner header', async () => {
  window.history.pushState({}, '', '/settings');
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url.includes('/api/me')) return Response.json({ authenticated: true, setupRequired: false });
    if (url.includes('/api/storage')) return Response.json({ totalBytes: 0 });
    if (url.includes('/api/shares')) return Response.json([]);
    return Response.json({});
  }));
  renderApp();

  const github = await screen.findByRole('link', { name: 'View Fileament on GitHub' });
  expect(github).toHaveAttribute('href', 'https://github.com/TechHutTV/fileament');
  expect(github).toHaveAttribute('target', '_blank');
  expect(github).toHaveAttribute('rel', 'noreferrer');
});

test('polishes empty collections and settings with active navigation and grouped states', async () => {
  window.history.pushState({}, '', '/collections');
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url.includes('/api/me')) return Response.json({ authenticated: true, setupRequired: false });
    if (url.includes('/api/collections') && init?.method === 'POST') return Response.json({ id: 'c1', name: 'Fixtures', slug: 'fixtures', description: '' }, { status: 201 });
    if (url.includes('/api/collections')) return Response.json([]);
    if (url.includes('/api/storage')) return Response.json({ totalBytes: 2048 });
    if (url.includes('/api/shares')) return Response.json([]);
    return Response.json({});
  }));
  renderApp();

  expect(await screen.findByRole('heading', { name: 'Collections' })).toBeInTheDocument();
  expect(await screen.findByText('No collections yet')).toBeInTheDocument();
  expect(screen.getByRole('link', { name: /collections/i })).toHaveAttribute('aria-current', 'page');
  fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'Fixtures' } });
  fireEvent.change(screen.getByLabelText('Description'), { target: { value: 'Useful parts' } });
  fireEvent.click(screen.getByRole('button', { name: /create collection/i }));
  await waitFor(() => expect(screen.getByLabelText('Name')).toHaveValue(''));

  window.history.pushState({}, '', '/settings');
  fireEvent(window, new Event('fileament:navigate'));
  expect(await screen.findByRole('heading', { name: 'Security' })).toBeInTheDocument();
  expect(await screen.findByText('2.0 KB')).toBeInTheDocument();
  expect(await screen.findByText('No share links yet')).toBeInTheDocument();
  expect(screen.getByRole('link', { name: /settings/i })).toHaveAttribute('aria-current', 'page');
});

test('renders selected collection cover thumbnails with a folder fallback', async () => {
  window.history.pushState({}, '', '/collections');
  let coverReady = false;
  let emitThumbnail: (() => void) | undefined;
  vi.stubGlobal('EventSource', class {
    addEventListener(type: string, listener: EventListener) {
      if (type === 'thumbnail') emitThumbnail = () => listener(new Event('thumbnail'));
    }
    close = vi.fn();
  });
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url.includes('/api/me')) return Response.json({ authenticated: true, setupRequired: false });
    if (url.includes('/api/collections')) return Response.json([
      { id: 'c1', name: 'Fixtures', slug: 'fixtures', description: '', coverModelId: 'm1', coverThumb: coverReady ? 'card.png' : undefined, modelIds: ['m1'] },
      { id: 'c2', name: 'Unsorted', slug: 'unsorted', description: '', modelIds: [] },
    ]);
    return Response.json({});
  }));
  renderApp();

  expect(await screen.findByRole('link', { name: /fixtures/i })).toBeInTheDocument();
  expect(screen.queryByRole('img', { name: 'Fixtures cover' })).not.toBeInTheDocument();
  coverReady = true;
  await act(async () => emitThumbnail?.());
  const cover = await screen.findByRole('img', { name: 'Fixtures cover' });
  expect(cover).toHaveAttribute('src', '/thumbs/m1/card.png');
  const fallback = screen.getByRole('link', { name: /unsorted/i }).querySelector('.collection-cover svg');
  expect(fallback).toBeInTheDocument();
});

test('persists the selected model color from settings', async () => {
  localStorage.removeItem('fileament-model-color');
  window.history.pushState({}, '', '/settings');
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url.includes('/api/me')) return Response.json({ authenticated: true, setupRequired: false });
    if (url.includes('/api/storage')) return Response.json({ totalBytes: 0 });
    if (url.includes('/api/shares')) return Response.json([]);
    return Response.json({});
  }));
  renderApp();

  const picker = await screen.findByLabelText('Model color');
  expect(picker).toHaveValue('#4f9f88');
  fireEvent.change(picker, { target: { value: '#c47742' } });
  expect(localStorage.getItem('fileament-model-color')).toBe('#c47742');

  fireEvent.click(screen.getByRole('button', { name: 'Use blue model color' }));
  expect(picker).toHaveValue('#4f7fb5');
  expect(localStorage.getItem('fileament-model-color')).toBe('#4f7fb5');
});

test('creates and downloads a sensitive Fileament backup from settings', async () => {
  window.history.pushState({}, '', '/settings');
  const calls: string[] = [];
  const createObjectURL = vi.fn(() => 'blob:fileament-backup');
  const revokeObjectURL = vi.fn();
  const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => undefined);
  vi.stubGlobal('URL', { ...URL, createObjectURL, revokeObjectURL });
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push(`${init?.method ?? 'GET'} ${url}`);
    if (url.includes('/api/me')) return Response.json({ authenticated: true, setupRequired: false });
    if (url.includes('/api/storage')) return Response.json({ totalBytes: 2048 });
    if (url.includes('/api/shares')) return Response.json([]);
    if (url === '/api/backups' && init?.method === 'POST') return new Response('backup bytes', { headers: { 'Content-Disposition': 'attachment; filename="fileament-backup-test.fileament"' } });
    return Response.json({});
  }));
  renderApp();

  fireEvent.click(await screen.findByRole('button', { name: /create and download backup/i }));
  await waitFor(() => expect(calls).toContain('POST /api/backups'));
  expect(createObjectURL).toHaveBeenCalled();
  expect(click).toHaveBeenCalled();
  expect(revokeObjectURL).toHaveBeenCalledWith('blob:fileament-backup');
  expect(await screen.findByText('Backup downloaded.')).toBeInTheDocument();
});

test('reviews and explicitly confirms a full restore before signing out', async () => {
  window.history.pushState({}, '', '/settings');
  const calls: string[] = [];
  let authenticated = true;
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push(`${init?.method ?? 'GET'} ${url}`);
    if (url.includes('/api/me')) return Response.json({ authenticated, setupRequired: false });
    if (url.includes('/api/storage')) return Response.json({ totalBytes: 2048 });
    if (url.includes('/api/shares')) return Response.json([]);
    if (url === '/api/backups/inspect' && init?.method === 'POST') return Response.json({
      restoreToken: 'review-token',
      manifest: { backupFormatVersion: 1, dataFormatVersion: 1, databaseVersion: 1, createdAt: '2026-08-03T15:33:45Z', models: 12, files: 34, collections: 5 },
    });
    if (url === '/api/backups/restore' && init?.method === 'POST') {
      authenticated = false;
      return Response.json({ ok: true });
    }
    return Response.json({});
  }));
  renderApp();

  const input = await screen.findByLabelText('Fileament backup');
  fireEvent.change(input, { target: { files: [new File(['backup'], 'library.fileament', { type: 'application/zip' })] } });
  fireEvent.click(screen.getByRole('button', { name: /review backup/i }));
  expect(await screen.findByText('12 models')).toBeInTheDocument();
  expect(screen.getByText('34 files')).toBeInTheDocument();
  expect(screen.getByText('5 collections')).toBeInTheDocument();
  expect(calls).not.toContain('POST /api/backups/restore');

  const restore = screen.getByRole('button', { name: /replace current data/i });
  expect(restore).toBeDisabled();
  fireEvent.change(screen.getByLabelText(/type restore to confirm/i), { target: { value: 'restore' } });
  expect(restore).toBeDisabled();
  fireEvent.change(screen.getByLabelText(/type restore to confirm/i), { target: { value: 'RESTORE' } });
  expect(restore).toBeEnabled();
  fireEvent.click(restore);

  await waitFor(() => expect(calls).toContain('POST /api/backups/restore'));
  expect(await screen.findByText('Owner login')).toBeInTheDocument();
});

test('reports a collections query failure instead of an empty state', async () => {
  window.history.pushState({}, '', '/collections');
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    if (String(input).includes('/api/me')) return Response.json({ authenticated: true, setupRequired: false });
    if (String(input).includes('/api/collections')) return new Response('failed', { status: 500 });
    return Response.json({});
  }));
  renderApp();

  expect(await screen.findByText('Collections could not be loaded')).toBeInTheDocument();
  expect(screen.queryByText('No collections yet')).not.toBeInTheDocument();
});

test('reports a share-links query failure instead of an empty state', async () => {
  window.history.pushState({}, '', '/settings');
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url.includes('/api/me')) return Response.json({ authenticated: true, setupRequired: false });
    if (url.includes('/api/storage')) return Response.json({ totalBytes: 0 });
    if (url.includes('/api/shares')) return new Response('failed', { status: 500 });
    return Response.json({});
  }));
  renderApp();

  expect(await screen.findByText('Share links could not be loaded')).toBeInTheDocument();
  expect(screen.queryByText('No share links yet')).not.toBeInTheDocument();
});

test('shows complete share metadata, copies the absolute URL, and only revokes active links', async () => {
  window.history.pushState({}, '', '/settings');
  const now = Math.floor(Date.now() / 1000);
  const calls: string[] = [];
  let finishRevoke: () => void = () => undefined;
  const revokeResponse = new Promise<Response>((resolve) => { finishRevoke = () => resolve(new Response(null, { status: 204 })); });
  const writeText = vi.fn().mockResolvedValue(undefined);
  vi.stubGlobal('navigator', Object.assign(Object.create(window.navigator), { clipboard: { writeText } }));
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push(`${init?.method ?? 'GET'} ${url}`);
    if (url.includes('/api/me')) return Response.json({ authenticated: true, setupRequired: false });
    if (url.includes('/api/storage')) return Response.json({ totalBytes: 0 });
    if (url === '/api/shares/active' && init?.method === 'DELETE') return revokeResponse;
    if (url.includes('/api/shares')) return Response.json([
      { id: 'active', token: 'active-token', url: 'https://models.example/s/active-token', scope: 'model', targetId: 'm1', targetName: 'Calibration Cube', label: 'Active link', createdAt: now - 3600, expiresAt: now + 86400, hitCount: 2 },
      { id: 'permanent', token: 'permanent-token', url: 'https://models.example/s/permanent-token', scope: 'collection', targetId: 'c1', targetName: 'Fixtures', label: 'Permanent link', createdAt: now - 7200, hitCount: 0 },
      { id: 'expired', token: 'expired-token', url: 'https://models.example/s/expired-token', scope: 'model', targetId: 'm1', targetName: 'Calibration Cube', label: 'Expired link', createdAt: now - 10800, expiresAt: now - 1, hitCount: 1 },
      { id: 'revoked', token: 'revoked-token', url: 'https://models.example/s/revoked-token', scope: 'model', targetId: 'm1', targetName: 'Calibration Cube', label: 'Revoked link', createdAt: now - 14400, revokedAt: now - 60, hitCount: 4 },
    ]);
    return Response.json({});
  }));
  renderApp();

  expect(await screen.findAllByText('Calibration Cube')).toHaveLength(3);
  expect(screen.getByText('Fixtures')).toBeInTheDocument();
  expect(screen.getByText('2 views')).toBeInTheDocument();
  expect(screen.getByText('1 view')).toBeInTheDocument();
  expect(screen.getAllByText('No expiration')).toHaveLength(2);
  expect(screen.getAllByText(/Created /)).toHaveLength(4);
  expect(screen.getAllByText('Active', { selector: '.share-status' })).toHaveLength(2);
  expect(screen.getByText('Expired', { selector: '.share-status' })).toBeInTheDocument();
  expect(screen.getByText('Revoked', { selector: '.share-status' })).toBeInTheDocument();
  expect(screen.getByRole('link', { name: /Active link/ })).toHaveAttribute('href', 'https://models.example/s/active-token');

  fireEvent.click(screen.getByRole('button', { name: 'Copy Active link share URL' }));
  await waitFor(() => expect(writeText).toHaveBeenCalledWith('https://models.example/s/active-token'));
  expect(await screen.findByText('Copied')).toBeInTheDocument();

  expect(screen.getByRole('button', { name: 'Revoke Active link share' })).toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Revoke Permanent link share' })).toBeInTheDocument();
  expect(screen.queryByRole('button', { name: 'Revoke Expired link share' })).not.toBeInTheDocument();
  expect(screen.queryByRole('button', { name: 'Revoke Revoked link share' })).not.toBeInTheDocument();

  fireEvent.click(screen.getByRole('button', { name: 'Revoke Active link share' }));
  const dialog = await screen.findByRole('alertdialog', { name: 'Revoke share link?' });
  expect(within(dialog).getByText(/Active link/)).toBeInTheDocument();
  expect(calls).not.toContain('DELETE /api/shares/active');
  fireEvent.click(within(dialog).getByRole('button', { name: 'Revoke link' }));
  await waitFor(() => expect(calls).toContain('DELETE /api/shares/active'));
  expect(within(dialog).getByRole('button', { name: 'Cancel' })).toBeDisabled();
  fireEvent.keyDown(document, { key: 'Tab' });
  expect(dialog).toHaveFocus();
  finishRevoke();
  await waitFor(() => expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument());
  expect(screen.queryByRole('button', { name: 'Revoke Active link share' })).not.toBeInTheDocument();
});

test('creates a share with no expiration when selected', async () => {
  window.history.pushState({}, '', '/models/m1');
  const requests: Array<{ method: string; body?: string }> = [];
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    requests.push({ method: init?.method ?? 'GET', body: typeof init?.body === 'string' ? init.body : undefined });
    if (url.includes('/api/me')) return Response.json({ authenticated: true, setupRequired: false });
    if (url.includes('/api/models/m1')) return Response.json(model);
    if (url.includes('/api/collections') || url.includes('/api/shares')) return Response.json([]);
    return Response.json({});
  }));
  renderApp();

  expect(await screen.findByDisplayValue('Calibration Cube')).toBeInTheDocument();
  fireEvent.click(screen.getByRole('checkbox', { name: 'No expiration' }));
  fireEvent.click(screen.getByRole('button', { name: 'Create share' }));

  await waitFor(() => {
    const request = requests.find((entry) => entry.method === 'POST' && entry.body?.includes('"scope":"model"'));
    expect(request).toBeDefined();
    expect(JSON.parse(request?.body ?? '{}')).toMatchObject({ expiresAt: 0 });
  });
});

test('detail management actions call owner APIs and preserve viewer gate', async () => {
  window.history.pushState({}, '', '/models/m1');
  const calls: string[] = [];
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push(`${init?.method ?? 'GET'} ${url}`);
    if (url.includes('/api/me')) return Response.json({ authenticated: true, setupRequired: false });
    if (url.includes('/api/models/m1')) return Response.json(model);
    if (url.includes('/api/collections')) return Response.json([{ id: 'c1', name: 'Fixtures', slug: 'fixtures', description: '', modelIds: ['m1'] }]);
    if (url.includes('/api/shares')) return Response.json([]);
    if (init?.method === 'PATCH' || init?.method === 'POST' || init?.method === 'DELETE' || init?.method === 'PUT') return Response.json(model);
    return Response.json({});
  }));
  renderApp();
  expect(await screen.findByDisplayValue('Calibration Cube')).toBeInTheDocument();
  expect(screen.getByRole('link', { name: 'Documentation' })).toHaveAttribute('href', 'https://example.com');
  expect(screen.getByRole('button', { name: /load 3d view/i })).toBeInTheDocument();
  const membership = screen.getByRole('checkbox', { name: 'Fixtures' });
  expect(membership).toBeChecked();
  fireEvent.click(membership);
  const dialog = await screen.findByRole('alertdialog', { name: 'Remove from collection?' });
  expect(within(dialog).getByText(/Calibration Cube.*Fixtures/)).toBeInTheDocument();
  expect(calls).not.toContain('DELETE /api/collections/c1/models/m1');
  fireEvent.click(within(dialog).getByRole('button', { name: 'Remove from collection' }));
  await waitFor(() => expect(calls).toContain('DELETE /api/collections/c1/models/m1'));
  fireEvent.change(screen.getByLabelText('Title'), { target: { value: 'Updated Cube' } });
  fireEvent.click(screen.getByRole('button', { name: /save changes/i }));
  await waitFor(() => expect(calls).toContain('PATCH /api/models/m1'));
  fireEvent.click(screen.getByRole('button', { name: /create share/i }));
  await waitFor(() => expect(calls).toContain('POST /api/shares'));
});

test('confirms model, variant, and image deletion before owner API calls', async () => {
  window.history.pushState({}, '', '/models/m1');
  const calls: string[] = [];
  const managedModel = { ...model, images: [{ id: 'i1', modelId: 'm1', relPath: 'images/reference.png', sortOrder: 0 }] };
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push(`${init?.method ?? 'GET'} ${url}`);
    if (url.includes('/api/me')) return Response.json({ authenticated: true, setupRequired: false });
    if (url.includes('/api/models/m1')) return Response.json(managedModel);
    if (url.includes('/api/collections') || url.includes('/api/shares')) return Response.json([]);
    return Response.json({ items: [], nextCursor: '' });
  }));
  renderApp();

  const deleteVariant = await screen.findByRole('button', { name: 'Delete cube.stl' });
  deleteVariant.focus();
  fireEvent.click(deleteVariant);
  let dialog = await screen.findByRole('alertdialog', { name: 'Delete variant?' });
  expect(within(dialog).getByText(/cube\.stl.*Calibration Cube/)).toBeInTheDocument();
  expect(calls).not.toContain('DELETE /api/models/m1/files/f1');
  const cancel = within(dialog).getByRole('button', { name: 'Cancel' });
  await waitFor(() => expect(cancel).toHaveFocus());
  fireEvent.keyDown(document, { key: 'Tab' });
  expect(within(dialog).getByRole('button', { name: 'Delete variant' })).toHaveFocus();
  fireEvent.keyDown(document, { key: 'Escape' });
  expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument();
  expect(deleteVariant).toHaveFocus();

  fireEvent.click(screen.getByRole('button', { name: 'Delete image from Calibration Cube' }));
  dialog = await screen.findByRole('alertdialog', { name: 'Delete image?' });
  expect(calls).not.toContain('DELETE /api/models/m1/images/i1');
  fireEvent.click(within(dialog).getByRole('button', { name: 'Delete image' }));
  await waitFor(() => expect(calls).toContain('DELETE /api/models/m1/images/i1'));

  fireEvent.click(screen.getByRole('button', { name: 'Delete model' }));
  dialog = await screen.findByRole('alertdialog', { name: 'Delete model?' });
  expect(within(dialog).getByText(/Calibration Cube.*variants.*images/)).toBeInTheDocument();
  expect(calls).not.toContain('DELETE /api/models/m1');
  fireEvent.click(within(dialog).getByRole('button', { name: 'Delete model' }));
  await waitFor(() => expect(calls).toContain('DELETE /api/models/m1'));
});

test('detail uses consistent variant terminology and promotes the selected download', async () => {
  window.history.pushState({}, '', '/models/m1');
  const secondFile = { ...model.files[0], id: 'f2', filename: 'bracket.3mf', relPath: 'files/bracket.3mf', format: '3mf' };
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url.includes('/api/me')) return Response.json({ authenticated: true, setupRequired: false });
    if (url.includes('/api/models/m1')) return Response.json({ ...model, files: [...model.files, secondFile] });
    if (url.includes('/api/collections') || url.includes('/api/shares')) return Response.json([]);
    return Response.json({});
  }));
  renderApp();

  const firstDownload = await screen.findByRole('link', { name: /download cube\.stl/i });
  expect(firstDownload).toHaveAttribute('href', '/files/m1/f1');
  expect(firstDownload).toHaveAttribute('download', 'cube.stl');
  expect(screen.getByRole('heading', { name: 'Variants and downloads' })).toBeInTheDocument();
  expect(screen.getByText('Add variants')).toBeInTheDocument();

  fireEvent.click(screen.getByRole('button', { name: /choose variant/i }));
  fireEvent.click(screen.getByRole('menuitemradio', { name: /bracket\.3mf/i }));
  const secondDownload = screen.getByRole('link', { name: /download bracket\.3mf/i });
  expect(secondDownload).toHaveAttribute('href', '/files/m1/f2');
  expect(secondDownload).toHaveAttribute('download', 'bracket.3mf');
});

test('renames a file variation inline and preserves its format', async () => {
  window.history.pushState({}, '', '/models/m1');
  let currentModel = { ...model };
  const calls: Array<{ method: string; url: string; body?: string }> = [];
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? 'GET';
    calls.push({ method, url, body: typeof init?.body === 'string' ? init.body : undefined });
    if (url.includes('/api/me')) return Response.json({ authenticated: true, setupRequired: false });
    if (url === '/api/models/m1/files/f1' && method === 'PATCH') {
      currentModel = { ...currentModel, files: [{ ...currentModel.files[0], filename: 'calibration-cube.stl' }] };
      return Response.json(currentModel);
    }
    if (url === '/api/models/m1') return Response.json(currentModel);
    if (url.includes('/api/collections') || url.includes('/api/shares')) return Response.json([]);
    return Response.json({});
  }));
  renderApp();

  fireEvent.click(await screen.findByRole('button', { name: 'Rename cube.stl' }));
  const input = screen.getByRole('textbox', { name: 'Variation name for cube.stl' });
  expect(input).toHaveValue('cube');
  fireEvent.change(input, { target: { value: 'calibration-cube' } });
  fireEvent.click(screen.getByRole('button', { name: 'Save variation name' }));

  await waitFor(() => expect(calls).toContainEqual({
    method: 'PATCH',
    url: '/api/models/m1/files/f1',
    body: JSON.stringify({ filename: 'calibration-cube.stl' }),
  }));
  expect(await screen.findByRole('link', { name: /download calibration-cube\.stl/i })).toHaveAttribute('download', 'calibration-cube.stl');
  expect(screen.getByRole('link', { name: 'calibration-cube.stl' })).toHaveAttribute('href', '/files/m1/f1');
});

test('detail places a visual variant picker below the selected download and updates the preview', async () => {
  window.history.pushState({}, '', '/models/m1');
  stubVariantDetail();
  renderApp();

  const actions = await screen.findByRole('group', { name: 'Selected variant' });
  const firstDownload = within(actions).getByRole('link', { name: /download cube\.stl/i });
  const picker = within(actions).getByRole('button', { name: /choose variant/i });
  const viewer = document.querySelector<HTMLElement>('.viewer');
  expect(viewer).not.toBeNull();
  expect(firstDownload.compareDocumentPosition(picker) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  expect(within(viewer!).getByRole('img', { name: 'cube.stl preview' })).toHaveAttribute('src', '/thumbs/m1/f1.png');

  fireEvent.click(picker);
  const menu = screen.getByRole('menu', { name: 'Variants' });
  const thumbnails = menu.querySelectorAll('img');
  expect(thumbnails).toHaveLength(2);
  expect(thumbnails[0]).toHaveAttribute('src', '/thumbs/m1/f1.png');
  expect(thumbnails[0]).toHaveAttribute('alt', '');
  expect(thumbnails[1]).toHaveAttribute('src', '/thumbs/m1/f2.png');
  expect(thumbnails[1]).toHaveAttribute('alt', '');
  fireEvent.click(within(menu).getByRole('menuitemradio', { name: /bracket\.3mf/i }));

  expect(within(actions).getByRole('link', { name: /download bracket\.3mf/i })).toHaveAttribute('href', '/files/m1/f2');
  expect(within(viewer!).getByRole('img', { name: 'bracket.3mf preview' })).toHaveAttribute('src', '/thumbs/m1/f2.png');
});

test('variant picker supports keyboard navigation and restores trigger focus', async () => {
  window.history.pushState({}, '', '/models/m1');
  stubVariantDetail();
  renderApp();

  const trigger = await screen.findByRole('button', { name: /choose variant/i });
  fireEvent.click(trigger);
  let menu = screen.getByRole('menu', { name: 'Variants' });
  let options = within(menu).getAllByRole('menuitemradio');
  await waitFor(() => expect(options[0]).toHaveFocus());

  fireEvent.keyDown(options[0], { key: 'ArrowDown' });
  expect(options[1]).toHaveFocus();
  fireEvent.keyDown(options[1], { key: 'Home' });
  expect(options[0]).toHaveFocus();
  fireEvent.keyDown(options[0], { key: 'End' });
  expect(options[1]).toHaveFocus();
  fireEvent.keyDown(options[1], { key: 'Escape' });
  expect(screen.queryByRole('menu', { name: 'Variants' })).not.toBeInTheDocument();
  expect(trigger).toHaveFocus();

  fireEvent.click(trigger);
  menu = screen.getByRole('menu', { name: 'Variants' });
  options = within(menu).getAllByRole('menuitemradio');
  await waitFor(() => expect(options[0]).toHaveFocus());
  fireEvent.click(options[1]);
  expect(screen.queryByRole('menu', { name: 'Variants' })).not.toBeInTheDocument();
  expect(trigger).toHaveFocus();
});

test('collection detail supports metadata, cover, ordering, and confirmed removal', async () => {
  window.history.pushState({}, '', '/collections/fixtures');
  const second = { ...model, id: 'm2', title: 'Second', files: [{ ...model.files[0], id: 'f2', modelId: 'm2' }] };
  const calls: string[] = [];
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push(`${init?.method ?? 'GET'} ${url}`);
    if (url.includes('/api/me')) return Response.json({ authenticated: true, setupRequired: false });
    if (url.includes('/api/collections/fixtures')) return Response.json({ id: 'c1', name: 'Fixtures', slug: 'fixtures', description: 'Useful parts', coverModelId: 'm1', modelIds: ['m1', 'm2'], models: [model, second] });
    if (init?.method === 'PATCH') return Response.json({ id: 'c1', name: 'Updated Fixtures', slug: 'fixtures', description: '', coverModelId: 'm2', modelIds: ['m1', 'm2'], models: [model, second] });
    return new Response(null, { status: 204 });
  }));
  renderApp();
  expect(await screen.findByDisplayValue('Fixtures')).toBeInTheDocument();
  fireEvent.change(screen.getByLabelText('Collection name'), { target: { value: 'Updated Fixtures' } });
  fireEvent.change(screen.getByLabelText('Cover model'), { target: { value: 'm2' } });
  fireEvent.click(screen.getByRole('button', { name: /save collection/i }));
  await waitFor(() => expect(calls).toContain('PATCH /api/collections/c1'));
  fireEvent.click(screen.getByRole('button', { name: /move calibration cube down/i }));
  await waitFor(() => expect(calls).toContain('PUT /api/collections/c1/order'));

  fireEvent.click(screen.getByRole('button', { name: 'Remove Calibration Cube from collection' }));
  let dialog = await screen.findByRole('alertdialog', { name: 'Remove from collection?' });
  expect(within(dialog).getByText(/Calibration Cube.*Fixtures/)).toBeInTheDocument();
  expect(calls).not.toContain('DELETE /api/collections/c1/models/m1');
  fireEvent.click(within(dialog).getByRole('button', { name: 'Remove from collection' }));
  await waitFor(() => expect(calls).toContain('DELETE /api/collections/c1/models/m1'));

  fireEvent.click(screen.getByRole('button', { name: /delete collection/i }));
  dialog = await screen.findByRole('alertdialog', { name: 'Delete collection?' });
  expect(within(dialog).getByText(/Fixtures.*models stay in your library/)).toBeInTheDocument();
  expect(calls).not.toContain('DELETE /api/collections/c1');
  fireEvent.click(within(dialog).getByRole('button', { name: 'Delete collection' }));
  await waitFor(() => expect(calls).toContain('DELETE /api/collections/c1'));
});

test('public collection cards select models within the same share and use token asset URLs', async () => {
  window.history.pushState({}, '', '/s/sharetoken?model=m1');
  let publicRequests = 0;
  let statusRequests = 0;
  let statusCode = 204;
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url === '/api/public/sharetoken/status') {
      statusRequests += 1;
      return new Response(null, { status: statusCode });
    }
    if (url === '/api/public/sharetoken') {
      publicRequests += 1;
      return Response.json({ share: { expiresAt: Math.floor(Date.now() / 1000) + 3600 }, collection: { id: 'c1', name: 'Shared', slug: 'shared', description: '', models: [{ ...model, primaryThumb: 'card.jpg' }] } });
    }
    return Response.json({});
  }));
  renderApp();
  expect(await screen.findByText('Shared')).toBeInTheDocument();
  expect(screen.getByRole('link', { name: 'Calibration Cube' })).toHaveAttribute('href', '/s/sharetoken?model=m1');
  expect(screen.getByRole('button', { name: /load 3d view/i })).toBeInTheDocument();
  expect(screen.getByRole('link', { name: /cube.stl/i })).toHaveAttribute('href', '/api/public/sharetoken/files/f1');
  await act(async () => {
    window.dispatchEvent(new Event('offline'));
    window.dispatchEvent(new Event('online'));
    await new Promise((resolve) => window.setTimeout(resolve, 0));
  });
  expect(publicRequests).toBe(1);
  expect(statusRequests).toBeGreaterThan(0);

  statusCode = 500;
  await act(async () => {
    window.dispatchEvent(new Event('focus'));
    await new Promise((resolve) => window.setTimeout(resolve, 0));
  });
  expect(screen.getByText('Shared')).toBeInTheDocument();

  statusCode = 410;
  await act(async () => {
    window.dispatchEvent(new Event('focus'));
    await new Promise((resolve) => window.setTimeout(resolve, 0));
  });
  expect(await screen.findByText('Share not available')).toBeInTheDocument();
  expect(screen.queryByText('Shared')).not.toBeInTheDocument();
  expect(publicRequests).toBe(1);
});

function renderApp() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(<QueryClientProvider client={client}><App /></QueryClientProvider>);
}

async function chooseSeparateModels() {
  fireEvent.click(await screen.findByRole('radio', { name: /separate models/i }));
}
