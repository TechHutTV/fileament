import { QueryClient, QueryClientProvider, useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Box, Check, ChevronDown, Download, Eye, EyeOff, Folder, HardDrive, Link2, Lock, Moon, Palette, Plus, Search, Settings, Sun, Trash2, Upload, X } from 'lucide-react';
import { Suspense, lazy, useEffect, useRef, useState, type KeyboardEvent as ReactKeyboardEvent, type ReactNode } from 'react';
import ReactMarkdown from 'react-markdown';
import { getModelColor, saveModelColor } from './viewerPreferences';

const ModelViewer = lazy(() => import('./Viewer'));
const VIEWER_LIMIT = 50 * 1024 * 1024;
const NAVIGATION_EVENT = 'fileament:navigate';
const client = new QueryClient();
const MODEL_COLOR_PRESETS = [
  ['green', '#4f9f88'],
  ['blue', '#4f7fb5'],
  ['orange', '#c47742'],
  ['red', '#a95353'],
  ['violet', '#7b68a6'],
  ['neutral', '#8a9290'],
] as const;

export type ModelFile = {
  id: string;
  modelId: string;
  filename: string;
  relPath: string;
  format: 'stl' | 'obj' | '3mf';
  sizeBytes: number;
  triangleCount: number;
  bboxX: number;
  bboxY: number;
  bboxZ: number;
  thumbPath?: string;
};

export type ModelImage = { id: string; modelId: string; relPath: string; sortOrder: number };
export type Model = {
  id: string;
  title: string;
  description: string;
  sourceUrl?: string;
  license?: string;
  author?: string;
  primaryThumb?: string;
  totalBytes: number;
  files: ModelFile[];
  images?: ModelImage[];
  tags?: string[];
};
type Page = { items: Model[]; nextCursor: string };
type Me = { authenticated: boolean; setupRequired: boolean };
type Collection = { id: string; name: string; slug: string; description: string; coverModelId?: string; modelIds?: string[]; models?: Model[] };
type Share = { id: string; token: string; scope: 'model' | 'collection'; targetId: string; label?: string; expiresAt?: number; revokedAt?: number };
type UploadStatus = 'queued' | 'uploading' | 'processing' | 'ready' | 'error' | 'removing';
type UploadOrganization = 'separate' | 'grouped';
type UploadItem = { key: string; file: File; files: File[]; title?: string; status: UploadStatus; collectionID?: string; model?: Model; error?: string };

export function Root() {
  return <QueryClientProvider client={client}><App /></QueryClientProvider>;
}

export function App() {
  const [path, setPath] = useState(window.location.pathname);
  useEffect(() => {
    const update = () => setPath(window.location.pathname);
    window.addEventListener('popstate', update);
    window.addEventListener(NAVIGATION_EVENT, update);
    return () => { window.removeEventListener('popstate', update); window.removeEventListener(NAVIGATION_EVENT, update); };
  }, []);
  if (path.startsWith('/s/')) return <PublicPage token={path.split('/')[2]} />;
  return <OwnerApp path={path} />;
}

function OwnerApp({ path }: { path: string }) {
  const [dark, setDark] = useState(localStorage.getItem('fileament-theme') === 'dark');
  useEffect(() => {
    document.documentElement.dataset.theme = dark ? 'dark' : 'light';
    localStorage.setItem('fileament-theme', dark ? 'dark' : 'light');
  }, [dark]);
  const me = useQuery<Me>({ queryKey: ['me'], queryFn: () => api('/api/me') });
  if (me.isLoading) return <Shell><Empty text="Loading" /></Shell>;
  if (me.data?.setupRequired) return <AuthScreen key="setup" mode="setup" />;
  if (!me.data?.authenticated) return <AuthScreen key="login" mode="login" />;
  return (
    <Shell>
      <nav className="topbar">
        <a className="brand" href="/"><Box size={22} />Fileament</a>
        <div className="navlinks">
          <a className={path === '/upload' ? 'active' : undefined} aria-current={path === '/upload' ? 'page' : undefined} href="/upload"><Upload size={18} />Upload</a>
          <a className={path.startsWith('/collections') ? 'active' : undefined} aria-current={path.startsWith('/collections') ? 'page' : undefined} href="/collections"><Folder size={18} />Collections</a>
          <a className={path === '/settings' ? 'active' : undefined} aria-current={path === '/settings' ? 'page' : undefined} href="/settings"><Settings size={18} />Settings</a>
          <button type="button" className="icon" onClick={() => setDark(!dark)} title="Toggle dark mode" aria-label="Toggle dark mode">{dark ? <Sun /> : <Moon />}</button>
        </div>
      </nav>
      {path.startsWith('/models/') ? <Detail id={path.split('/')[2]} /> : path.startsWith('/collections/') ? <CollectionDetail slug={path.split('/')[2]} /> : path === '/collections' ? <CollectionsPage /> : path === '/upload' ? <UploadPage /> : path === '/settings' ? <SettingsPage /> : <Catalog />}
    </Shell>
  );
}

function Shell({ children }: { children: ReactNode }) {
  return <main className="app">{children}</main>;
}

function AuthScreen({ mode }: { mode: 'setup' | 'login' }) {
  const [password, setPassword] = useState('');
  const [showPassword, setShowPassword] = useState(false);
  const qc = useQueryClient();
  const mutation = useMutation({
    mutationFn: () => api(mode === 'setup' ? '/api/auth/setup' : '/api/auth/login', { method: 'POST', body: JSON.stringify({ password }) }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['me'] }),
  });
  return (
    <main className="auth">
      <div className="auth-shell">
        <a className="auth-brand" href="/"><span><Box size={25} /></span><strong>Fileament</strong></a>
        <form className="auth-card" onSubmit={(e) => { e.preventDefault(); mutation.mutate(); }}>
          <div className="auth-heading">
            <span className="eyebrow">Private model library</span>
            <h1>{mode === 'setup' ? 'Set owner password' : 'Owner login'}</h1>
            <p>{mode === 'setup' ? 'Create the password that protects your library and private model files.' : 'Welcome back. Enter your owner password to access your library.'}</p>
          </div>
          <label>Password<div className="password-control"><input type={showPassword ? 'text' : 'password'} minLength={mode === 'setup' ? 12 : undefined} value={password} onChange={(e) => setPassword(e.target.value)} autoFocus /><button type="button" className="icon" onClick={() => setShowPassword((visible) => !visible)} aria-label={showPassword ? 'Hide password' : 'Show password'} title={showPassword ? 'Hide password' : 'Show password'}>{showPassword ? <EyeOff size={18} /> : <Eye size={18} />}</button></div></label>
          {mode === 'setup' && <small className="field-help">Use at least 12 characters.</small>}
          <button type="submit" disabled={mutation.isPending}>{mutation.isPending ? 'Please wait' : mode === 'setup' ? 'Create owner' : 'Log in'}</button>
          {mutation.isError && <p className="error auth-error">Authentication failed. Check your password and try again.</p>}
        </form>
        <p className="auth-tagline">Your 3D files, organized.</p>
      </div>
    </main>
  );
}

function Catalog() {
  const [q, setQ] = useState('');
  const [tag, setTag] = useState('');
  const [collection, setCollection] = useState('');
  const [sort, setSort] = useState('created');
  const debounced = useDebounced(q, 250);
  const qc = useQueryClient();
  const collections = useQuery<Collection[]>({ queryKey: ['collections'], queryFn: () => api('/api/collections') });
  const tags = useQuery<{ name: string; slug: string }[]>({ queryKey: ['tags'], queryFn: () => api('/api/tags') });
  const collectionItems = Array.isArray(collections.data) ? collections.data : [];
  const tagItems = Array.isArray(tags.data) ? tags.data : [];
  const page = useInfiniteQuery({
    queryKey: ['models', debounced, tag, collection, sort],
    initialPageParam: '',
    queryFn: ({ pageParam }) => {
      const query = new URLSearchParams({ limit: '24', sort });
      if (debounced) query.set('q', debounced);
      if (tag) query.set('tag', tag);
      if (collection) query.set('collection', collection);
      if (pageParam) query.set('cursor', pageParam);
      return api(`/api/models?${query}`) as Promise<Page>;
    },
    getNextPageParam: (lastPage) => lastPage.nextCursor || undefined,
  });
  const items = [...new Map((page.data?.pages.flatMap((result) => result.items) ?? []).map((item) => [item.id, item])).values()];
  useEffect(() => {
    if (typeof EventSource === 'undefined') return undefined;
    const es = new EventSource('/api/events');
    es.addEventListener('thumbnail', () => qc.invalidateQueries({ queryKey: ['models'] }));
    return () => es.close();
  }, [qc]);
  const hasFilters = q || tag || collection || sort !== 'created';
  return (
    <section className="content page-content">
      <PageHeader eyebrow="Your library" title="Models" description={`${items.length} ${items.length === 1 ? 'model' : 'models'} shown`} action={<a className="button-link" href="/upload"><Plus size={17} />Add models</a>} />
      <div className="toolbar multi catalog-toolbar">
        <label className="search"><Search size={18} /><input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search models" aria-label="Search models" /></label>
        <Select compact label="Tag" value={tag} onChange={setTag} options={[['', 'All tags'], ...tagItems.map((t) => [t.slug, t.name] as [string, string])]} />
        <Select compact label="Collection" value={collection} onChange={setCollection} options={[['', 'All collections'], ...collectionItems.map((c) => [c.slug, c.name] as [string, string])]} />
        <Select compact label="Sort" value={sort} onChange={setSort} options={[['created', 'Newest'], ['updated', 'Updated'], ['title', 'Title'], ['size', 'Size']]} />
        {hasFilters && <button type="button" className="clear-filters" onClick={() => { setQ(''); setTag(''); setCollection(''); setSort('created'); }}><X size={16} />Clear</button>}
      </div>
      {page.isError && <Empty text="Catalog could not be loaded" />}
      {page.isLoading && items.length === 0 && <Empty text="Loading catalog" />}
      {!page.isLoading && items.length === 0 && <Empty text="No models found" />}
      <div className="grid">{items.map((model) => <ModelCard key={model.id} model={model} />)}</div>
      {page.hasNextPage && <button type="button" className="load" disabled={page.isFetchingNextPage} onClick={() => { void page.fetchNextPage(); }}>{page.isFetchingNextPage ? 'Loading more' : 'Load more'}</button>}
    </section>
  );
}

function ModelCard({ model }: { model: Model }) {
  const src = model.primaryThumb ? `/thumbs/${model.id}/${model.primaryThumb}` : '';
  const file = model.files[0];
  return (
    <a className="card" href={`/models/${model.id}`}>
      <div className="thumb">{src ? <LazyImage src={src} alt={`${model.title} thumbnail`} /> : <Box size={42} aria-hidden />}{file && <span className="card-format">{file.format.toUpperCase()}</span>}</div>
      <div className="card-body"><h2>{model.title}</h2><p className="card-meta"><span>{formatBytes(model.totalBytes)}</span>{file && <span>{file.triangleCount.toLocaleString()} tris</span>}</p></div>
    </a>
  );
}

function Detail({ id }: { id: string }) {
  const qc = useQueryClient();
  const { data: model, isLoading, isError } = useQuery<Model>({ queryKey: ['model', id], queryFn: () => api(`/api/models/${id}`) });
  const collections = useQuery<Collection[]>({ queryKey: ['collections'], queryFn: () => api('/api/collections') });
  const shares = useQuery<Share[]>({ queryKey: ['shares'], queryFn: () => api('/api/shares') });
  const [selectedFileID, setSelectedFileID] = useState('');
  const [forceViewer, setForceViewer] = useState(false);
  useEffect(() => { if (model?.files?.[0] && !selectedFileID) setSelectedFileID(model.files[0].id); }, [model, selectedFileID]);
  const file = model?.files.find((f) => f.id === selectedFileID) ?? model?.files?.[0];
  const canAutoLoad = !!file && file.sizeBytes <= VIEWER_LIMIT;
  const previewThumb = fileThumbName(file) || model?.primaryThumb;
  const invalidate = () => { qc.invalidateQueries({ queryKey: ['model', id] }); qc.invalidateQueries({ queryKey: ['models'] }); qc.invalidateQueries({ queryKey: ['storage'] }); };
  const patch = useMutation({ mutationFn: (body: Partial<Model>) => api(`/api/models/${id}`, { method: 'PATCH', body: JSON.stringify(body) }), onSuccess: invalidate });
  const removeModel = useMutation({ mutationFn: () => api(`/api/models/${id}`, { method: 'DELETE' }), onSuccess: () => navigate('/') });
  const deleteFile = useMutation({ mutationFn: (fid: string) => api(`/api/models/${id}/files/${fid}`, { method: 'DELETE' }), onSuccess: invalidate });
  const deleteImage = useMutation({ mutationFn: (imageID: string) => api(`/api/models/${id}/images/${imageID}`, { method: 'DELETE' }), onSuccess: invalidate });
  const setThumb = useMutation({ mutationFn: (fileId: string) => api(`/api/models/${id}/thumb`, { method: 'PUT', body: JSON.stringify({ fileId }) }), onSuccess: invalidate });
  const share = useMutation({ mutationFn: (body: { label: string; expiresAt: number }) => api('/api/shares', { method: 'POST', body: JSON.stringify({ scope: 'model', targetId: id, ...body }) }), onSuccess: () => qc.invalidateQueries({ queryKey: ['shares'] }) });
  if (isLoading) return <section className="content"><Empty text="Loading model" /></section>;
  if (isError || !model) return <section className="content"><Empty text="Model not found" /></section>;
  return (
    <section className="detail">
      <div className="viewer">
        {file && (canAutoLoad || forceViewer) ? <Suspense fallback={<Empty text="Loading view" />}><ModelViewer key={file.id} file={file} url={`/mesh/${model.id}/${file.id}`} /></Suspense> : <div className="static-thumb">{previewThumb ? <img src={`/thumbs/${model.id}/${previewThumb}`} alt={`${file?.filename ?? model.title} preview`} /> : <Box size={64} aria-hidden />}{file && <button type="button" onClick={() => setForceViewer(true)}>Load 3D view</button>}</div>}
      </div>
      <aside className="panel">
        {file && <div className="selected-variant-actions" role="group" aria-label="Selected variant">
          <a className="model-download-primary" href={`/files/${model.id}/${file.id}`} download={file.filename}><span className="model-download-icon"><Download size={22} /></span><span className="model-download-copy"><strong>Download {file.filename}</strong><small>{file.format.toUpperCase()} · {formatBytes(file.sizeBytes)}</small></span></a>
          {model.files.length > 1 && <VariantPicker files={model.files} selectedFileID={file.id} onSelect={(fileID) => { setSelectedFileID(fileID); setForceViewer(false); }} thumbnailURL={(variant) => { const thumb = fileThumbName(variant); return thumb ? `/thumbs/${model.id}/${thumb}` : ''; }} />}
        </div>}
        <ModelEditor model={model} onSave={(body) => patch.mutate(body)} />
        <Markdown text={model.description} />
        <div className="meta">{model.author && <span>By {model.author}</span>}{model.license && <span>{model.license}</span>}{model.sourceUrl && <a href={model.sourceUrl}>Source</a>}</div>
        <div className="tags">{model.tags?.map((t) => <span key={t}>{t}</span>)}</div>
        <h2>Variants and downloads</h2>
        {model.files.map((f) => <div className="file model-file" key={f.id}><div className="model-file-copy"><a href={`/files/${model.id}/${f.id}`}><Download size={16} /><span>{f.filename}</span></a><small>{f.triangleCount} tris · {dims(f)} · {formatBytes(f.sizeBytes)}</small></div><div className="model-file-actions"><button type="button" className="icon" title="Use thumbnail" onClick={() => setThumb.mutate(f.id)}><Check size={16} /></button><button type="button" className="icon danger" title="Delete file" onClick={() => deleteFile.mutate(f.id)}><Trash2 size={16} /></button></div></div>)}
        <UploadInline label="Add variants" path={`/api/models/${id}/files`} onDone={invalidate} />
        <h2>Images</h2>
        <div className="images">{model.images?.map((img) => <figure key={img.id}><img src={`/images/${model.id}/${img.id}`} alt={`${model.title} image`} /><button type="button" className="icon danger" onClick={() => deleteImage.mutate(img.id)}><X size={16} /></button></figure>)}</div>
        <UploadInline label="Add images" path={`/api/models/${id}/images`} onDone={invalidate} />
        <h2>Collections</h2>
        <CollectionMembership collections={collections.data ?? []} model={model} />
        <h2>Share</h2>
        <ShareForm onCreate={(body) => share.mutate(body)} />
        {shares.data?.filter((s) => s.scope === 'model' && s.targetId === id && !s.revokedAt).map((s) => <ShareRow key={s.id} share={s} />)}
        <button type="button" className="danger" onClick={() => removeModel.mutate()}><Trash2 size={16} />Delete model</button>
      </aside>
    </section>
  );
}

function ModelEditor({ model, onSave }: { model: Model; onSave: (body: Partial<Model>) => void }) {
  const [title, setTitle] = useState(model.title);
  const [description, setDescription] = useState(model.description);
  const [sourceUrl, setSourceUrl] = useState(model.sourceUrl ?? '');
  const [license, setLicense] = useState(model.license ?? '');
  const [author, setAuthor] = useState(model.author ?? '');
  const [tags, setTags] = useState((model.tags ?? []).join(', '));
  return (
    <form className="stack" onSubmit={(e) => { e.preventDefault(); onSave({ title, description, sourceUrl, license, author, tags: tags.split(',').map((t) => t.trim()).filter(Boolean) }); }}>
      <label>Title<input value={title} onChange={(e) => setTitle(e.target.value)} /></label>
      <label>Notes<textarea value={description} onChange={(e) => setDescription(e.target.value)} /></label>
      <label>Source URL<input value={sourceUrl} onChange={(e) => setSourceUrl(e.target.value)} /></label>
      <label>License<input value={license} onChange={(e) => setLicense(e.target.value)} /></label>
      <label>Author<input value={author} onChange={(e) => setAuthor(e.target.value)} /></label>
      <label>Tags<input value={tags} onChange={(e) => setTags(e.target.value)} /></label>
      <button type="submit"><Check size={16} />Save changes</button>
    </form>
  );
}

function UploadPage() {
  const qc = useQueryClient();
  const [items, setItems] = useState<UploadItem[]>([]);
  const [dragging, setDragging] = useState(false);
  const [collectionID, setCollectionID] = useState('');
  const [organization, setOrganization] = useState<UploadOrganization | ''>('');
  const [groupedTitle, setGroupedTitle] = useState('');
  const collections = useQuery<Collection[]>({ queryKey: ['collections'], queryFn: () => api('/api/collections') });
  const collectionItems = Array.isArray(collections.data) ? collections.data : [];
  const sequence = useRef(0);
  const input = useRef<HTMLInputElement>(null);
  const pending = useRef(new Set<string>());
  const cancelled = useRef(new Set<string>());
  const uploadChain = useRef<Promise<void>>(Promise.resolve());
  const organizationSelected = organization !== '';

  const updateItem = (key: string, update: Partial<UploadItem>) => {
    setItems((current) => current.map((item) => item.key === key ? { ...item, ...update } : item));
  };

  const uploadItem = async (item: UploadItem) => {
    updateItem(item.key, { status: 'uploading' });
    const body = new FormData();
    const grouped = item.files.length > 1;
    if (grouped) {
      body.append('title', item.title ?? '');
      item.files.forEach((file) => body.append('files', file));
    } else {
      body.append('file', item.file);
    }
    try {
      const model = await api(grouped ? '/api/models/grouped' : '/api/models', { method: 'POST', body }) as Model;
      const discardIfCancelled = async () => {
        if (!cancelled.current.has(item.key)) return false;
        try {
          await api(`/api/models/${model.id}`, { method: 'DELETE' });
        } catch {
          const failed = { ...item, model, status: 'error' as const, error: 'Upload completed, but could not remove the model.' };
          setItems((current) => current.some((candidate) => candidate.key === item.key)
            ? current.map((candidate) => candidate.key === item.key ? failed : candidate)
            : [...current, failed]);
          qc.invalidateQueries({ queryKey: ['models'] });
          qc.invalidateQueries({ queryKey: ['storage'] });
        }
        qc.invalidateQueries({ queryKey: ['collections'] });
        return true;
      };
      if (await discardIfCancelled()) return;
      let collectionError: string | undefined;
      let collectionChanged = false;
      if (item.collectionID) {
        try {
          await api(`/api/collections/${item.collectionID}/models/${model.id}`, { method: 'PUT' });
          collectionChanged = true;
        } catch {
          collectionError = 'Uploaded, but could not add this model to the collection.';
        }
      }
      if (await discardIfCancelled()) return;
      if (collectionChanged) qc.invalidateQueries({ queryKey: ['collections'] });
      updateItem(item.key, { model, status: model.primaryThumb ? 'ready' : 'processing', error: collectionError });
      if (!model.primaryThumb) {
        try {
          const refreshed = await api(`/api/models/${model.id}`) as Model;
          if (refreshed.primaryThumb) updateItem(item.key, { model: refreshed, status: 'ready' });
        } catch {
          // Thumbnail events continue to reconcile uploads if this refresh races rendering.
        }
      }
      qc.invalidateQueries({ queryKey: ['models'] });
      qc.invalidateQueries({ queryKey: ['storage'] });
    } catch {
      if (!cancelled.current.has(item.key)) {
        updateItem(item.key, { status: 'error', error: 'Upload failed. Remove it and try again.' });
      }
    } finally {
      pending.current.delete(item.key);
      cancelled.current.delete(item.key);
    }
  };

  const addFiles = (files: FileList | File[]) => {
    if (!organization) return;
    const selected = Array.from(files);
    const loose = selected.filter((file) => !file.name.toLowerCase().endsWith('.zip'));
    const groupedLoose = organization === 'grouped' && loose.length > 1;
    let looseQueued = false;
    const batches = selected.flatMap((file) => {
      if (file.name.toLowerCase().endsWith('.zip') || !groupedLoose) return [[file]];
      if (looseQueued) return [];
      looseQueued = true;
      return [loose];
    });
    const queued = batches.map((batch) => ({
      key: `upload-${++sequence.current}`,
      file: batch[0],
      files: batch,
      title: batch.length > 1 ? groupedTitle.trim() || undefined : undefined,
      status: 'queued' as const,
      collectionID: collectionID || undefined,
    }));
    if (queued.length === 0) return;
    queued.forEach((item) => pending.current.add(item.key));
    setItems((current) => [...current, ...queued]);
    queued.forEach((item) => {
      uploadChain.current = uploadChain.current.then(async () => {
        if (cancelled.current.has(item.key)) {
          pending.current.delete(item.key);
          cancelled.current.delete(item.key);
          return;
        }
        await uploadItem(item);
      });
    });
  };

  const removeItem = async (item: UploadItem) => {
    cancelled.current.add(item.key);
    if (!item.model) {
      setItems((current) => current.filter((candidate) => candidate.key !== item.key));
      return;
    }
    updateItem(item.key, { status: 'removing' });
    try {
      await api(`/api/models/${item.model.id}`, { method: 'DELETE' });
      setItems((current) => current.filter((candidate) => candidate.key !== item.key));
      qc.invalidateQueries({ queryKey: ['models'] });
      qc.invalidateQueries({ queryKey: ['storage'] });
      qc.invalidateQueries({ queryKey: ['collections'] });
    } catch {
      updateItem(item.key, { status: 'error', error: 'Could not remove this model.' });
    } finally {
      cancelled.current.delete(item.key);
    }
  };

  useEffect(() => {
    if (typeof EventSource === 'undefined') return undefined;
    const events = new EventSource('/api/events');
    events.addEventListener('thumbnail', (event) => {
      const modelID = JSON.parse((event as MessageEvent).data).modelId as string;
      void api(`/api/models/${modelID}`).then((model: Model) => {
        setItems((current) => current.map((item) => item.model?.id === modelID ? { ...item, model, status: model.primaryThumb ? 'ready' : 'processing' } : item));
      }).catch(() => undefined);
    });
    return () => events.close();
  }, []);

  useEffect(() => () => { pending.current.forEach((key) => cancelled.current.add(key)); }, []);

  const active = items.some((item) => item.status === 'queued' || item.status === 'uploading' || item.status === 'removing');
  const completed = items.filter((item) => item.status === 'ready' || item.status === 'processing').length;
  return <section className="upload-page">
    <header className="upload-header">
      <span className="eyebrow">Add models</span>
      <h1>Build your library</h1>
      <p>Choose how loose files become models. ZIP bundles always stay together as one model with variants.</p>
    </header>
    <fieldset className="upload-organization">
      <legend>Loose file organization</legend>
      <div className="upload-organization-options">
        <label className={organization === 'separate' ? 'selected' : ''}>
          <input type="radio" name="upload-organization" value="separate" checked={organization === 'separate'} onChange={() => setOrganization('separate')} />
          <span><strong>Separate models</strong><small>One library model per loose file</small></span>
        </label>
        <label className={organization === 'grouped' ? 'selected' : ''}>
          <input type="radio" name="upload-organization" value="grouped" checked={organization === 'grouped'} onChange={() => setOrganization('grouped')} />
          <span><strong>One model with variants</strong><small>Group multiple loose files into one model</small></span>
        </label>
      </div>
      {organization === 'grouped' && <label className="grouped-model-title"><span>Model name <small>Optional</small></span><input aria-label="Grouped model name" value={groupedTitle} placeholder="Uses the first variant name" onChange={(event) => setGroupedTitle(event.target.value)} /></label>}
      <small className="upload-organization-note">Choose an option before adding files. This applies only to loose STL, OBJ, and 3MF files; every ZIP remains its own model.</small>
    </fieldset>
    <label className="upload-collection">
      <span className="upload-collection-icon"><Folder size={20} /></span>
      <span className="upload-collection-copy"><strong>Organize uploads</strong><small>The selected collection applies to the next files you add.</small></span>
      <select aria-label="Add uploads to collection" value={collectionID} onChange={(event) => setCollectionID(event.target.value)}>
        <option value="">Library only</option>
        {collectionItems.map((item) => <option value={item.id} key={item.id}>{item.name}</option>)}
      </select>
    </label>
    <div
      className={`upload-dropzone${dragging ? ' dragging' : ''}${organizationSelected ? '' : ' disabled'}`}
      role="button"
      tabIndex={organizationSelected ? 0 : -1}
      aria-disabled={!organizationSelected}
      aria-label={organizationSelected ? 'Drop 3D files or choose files' : 'Choose file organization before adding files'}
      onClick={() => { if (organizationSelected) input.current?.click(); }}
      onKeyDown={(event) => { if (organizationSelected && (event.key === 'Enter' || event.key === ' ')) { event.preventDefault(); input.current?.click(); } }}
      onDragEnter={(event) => { event.preventDefault(); if (organizationSelected) setDragging(true); }}
      onDragOver={(event) => { event.preventDefault(); if (organizationSelected) setDragging(true); }}
      onDragLeave={(event) => { if (!event.currentTarget.contains(event.relatedTarget as Node)) setDragging(false); }}
      onDrop={(event) => { event.preventDefault(); setDragging(false); if (organizationSelected) addFiles(event.dataTransfer.files); }}
    >
      <input ref={input} className="visually-hidden" type="file" multiple accept=".stl,.obj,.3mf,.zip" aria-label="Choose 3D files" disabled={!organizationSelected} onChange={(event) => { if (event.target.files) addFiles(event.target.files); event.target.value = ''; }} />
      <span className="dropzone-icon"><Upload size={28} /></span>
      <strong>{!organizationSelected ? 'Choose file organization first' : dragging ? 'Drop to start uploading' : 'Drop 3D files here'}</strong>
      <span>{organizationSelected ? 'or click to browse your files' : 'Select one of the options above to enable uploads'}</span>
      <small>STL, OBJ, 3MF, and ZIP bundles</small>
    </div>
    {items.length > 0 && <section className="upload-queue" aria-live="polite">
      <div className="queue-heading"><div><span className="eyebrow">Upload queue</span><h2>{items.length} {items.length === 1 ? 'model' : 'models'}</h2></div><span>{completed} uploaded</span></div>
      <div className="upload-grid">{items.map((item) => {
        const itemName = item.model?.title || item.title || item.file.name;
        const itemBytes = item.files.reduce((total, file) => total + file.size, 0);
        const thumbnail = item.model?.primaryThumb ? `/thumbs/${item.model.id}/${item.model.primaryThumb}` : '';
        const label = item.status === 'queued' ? 'Queued' : item.status === 'uploading' ? 'Uploading' : item.status === 'processing' ? 'Generating preview' : item.status === 'ready' ? 'Ready' : item.status === 'removing' ? 'Removing' : 'Needs attention';
        const placeholder = item.status === 'queued' ? 'Waiting to upload' : item.status === 'uploading' ? 'Uploading model' : item.status === 'error' ? 'Upload failed' : item.status === 'removing' ? 'Removing model' : 'Rendering preview';
        return <article className={`upload-card ${item.status}`} key={item.key}>
          <div className="upload-preview">{thumbnail ? <img src={thumbnail} alt={`${itemName} thumbnail`} /> : <div className="upload-placeholder"><Box size={38} /><span>{placeholder}</span></div>}{item.status === 'uploading' && <span className="upload-progress" />}</div>
          <button type="button" className="icon upload-remove" aria-label={`${item.model ? 'Remove' : 'Cancel'} ${itemName}${item.model ? '' : ' upload'}`} onClick={() => { void removeItem(item); }} disabled={item.status === 'removing'}><X size={17} /></button>
          <div className="upload-card-body"><div><h3>{itemName}</h3><p>{item.files.length > 1 ? `${item.files.length} variants` : item.file.name} · {formatBytes(itemBytes)}</p></div><span className={`upload-status ${item.status}`}>{item.status === 'ready' && <Check size={13} />}{label}</span>{item.error && <p className="upload-error">{item.error}</p>}</div>
        </article>;
      })}</div>
    </section>}
    {items.length > 0 && <footer className="upload-finish"><div><strong>{active ? 'Uploads in progress' : `${completed} ${completed === 1 ? 'model' : 'models'} added`}</strong><span>{active ? 'You can keep adding files while these finish.' : 'Everything is saved to your library.'}</span></div><button type="button" disabled={active} onClick={() => navigate('/')}><Check size={18} />Finish and view library</button></footer>}
  </section>;
}

function SettingsPage() {
  const qc = useQueryClient();
  const { data } = useQuery<{ totalBytes: number }>({ queryKey: ['storage'], queryFn: () => api('/api/storage') });
  const { data: shares, isLoading: sharesLoading, isError: sharesError } = useQuery<Share[]>({ queryKey: ['shares'], queryFn: () => api('/api/shares') });
  const revoke = useMutation({ mutationFn: (id: string) => api(`/api/shares/${id}`, { method: 'DELETE' }), onSuccess: () => qc.invalidateQueries({ queryKey: ['shares'] }) });
  const [currentPassword, setCurrentPassword] = useState('');
  const [newPassword, setNewPassword] = useState('');
  const [modelColor, setModelColor] = useState(getModelColor);
  const chooseModelColor = (color: string) => setModelColor(saveModelColor(color));
  const change = useMutation({ mutationFn: () => api('/api/auth/password', { method: 'POST', body: JSON.stringify({ currentPassword, newPassword }) }), onSuccess: () => { setCurrentPassword(''); setNewPassword(''); } });
  return <section className="content narrow settings-page">
    <PageHeader eyebrow="Owner controls" title="Settings" description="Manage appearance, storage, account security, and public access to your library." />
    <div className="storage-card"><span className="surface-icon"><HardDrive size={20} /></span><div><span>Library storage</span><strong>{formatBytes(data?.totalBytes ?? 0)}</strong></div></div>
    <section className="surface-card settings-card">
      <SectionHeading icon={<Palette size={19} />} title="Viewer" description="Choose the material color used for STL and OBJ model previews on this browser." />
      <div className="model-color-setting">
        <label className="model-color-picker"><input type="color" aria-label="Model color" value={modelColor} onChange={(event) => chooseModelColor(event.target.value)} /><span><strong>Model color</strong><small>{modelColor.toUpperCase()}</small></span></label>
        <div className="model-color-presets" role="group" aria-label="Model color presets">{MODEL_COLOR_PRESETS.map(([name, color]) => <button type="button" className="model-color-preset" aria-label={`Use ${name} model color`} aria-pressed={modelColor === color} title={name} style={{ backgroundColor: color }} onClick={() => chooseModelColor(color)} key={color} />)}</div>
      </div>
    </section>
    <section className="surface-card settings-card">
      <SectionHeading icon={<Lock size={19} />} title="Security" description="Update the password used to access this private library." />
      <form className="stack settings-form" onSubmit={(e) => { e.preventDefault(); change.mutate(); }}><label>Current password<input type="password" value={currentPassword} onChange={(e) => setCurrentPassword(e.target.value)} /></label><label>New password<input type="password" minLength={12} value={newPassword} onChange={(e) => setNewPassword(e.target.value)} /></label><small className="field-help">Use at least 12 characters.</small><button type="submit" disabled={change.isPending}>{change.isPending ? 'Updating password' : 'Change password'}</button>{change.isError && <p className="error">Password change failed</p>}{change.isSuccess && <p className="success">Password updated.</p>}</form>
    </section>
    <section className="surface-card settings-card">
      <SectionHeading icon={<Link2 size={19} />} title="Share links" description="Review and revoke public links created for models and collections." />
      {sharesError && <Empty text="Share links could not be loaded" />}
      {sharesLoading && <Empty text="Loading share links" />}
      {!sharesLoading && !sharesError && (shares?.length ?? 0) === 0 && <EmptyState icon={<Link2 size={24} />} title="No share links yet" text="Links you create from a model or collection will appear here." compact />}
      {shares?.map((s) => <div className="file share-row" key={s.id}><a href={`/s/${s.token}`}><Link2 size={16} />{s.label || s.scope}</a><small>{s.revokedAt ? 'Revoked' : s.expiresAt ? new Date(s.expiresAt * 1000).toLocaleDateString() : 'No expiry'}</small><button type="button" className="icon danger" aria-label={`Revoke ${s.label || s.scope} share`} onClick={() => revoke.mutate(s.id)}><Trash2 size={16} /></button></div>)}
    </section>
  </section>;
}

function CollectionsPage() {
  const qc = useQueryClient();
  const [formVersion, setFormVersion] = useState(0);
  const { data, isLoading, isError } = useQuery<Collection[]>({ queryKey: ['collections'], queryFn: () => api('/api/collections') });
  const create = useMutation({ mutationFn: (body: Partial<Collection>) => api('/api/collections', { method: 'POST', body: JSON.stringify(body) }), onSuccess: () => { setFormVersion((version) => version + 1); qc.invalidateQueries({ queryKey: ['collections'] }); } });
  return <section className="content page-content collections-page">
    <PageHeader eyebrow="Organize your library" title="Collections" description="Group related models into focused sets for projects, printers, or workflows." />
    <section className="surface-card collection-create">
      <SectionHeading icon={<Folder size={19} />} title="Create a collection" description="Start a new group and add models from their detail pages." />
      <CollectionForm key={formVersion} onSave={(body) => create.mutate(body)} />
    </section>
    {isError && <Empty text="Collections could not be loaded" />}
    {isLoading && <Empty text="Loading collections" />}
    {!isLoading && !isError && (data?.length ?? 0) === 0 && <EmptyState icon={<Folder size={28} />} title="No collections yet" text="Create your first collection above, then add models from your library." />}
    <div className="grid collection-grid">{(data ?? []).map((c) => { const count = c.modelIds?.length ?? c.models?.length ?? 0; return <a className="card collection-card" href={`/collections/${c.slug}`} key={c.id}><div className="collection-cover"><Folder size={34} aria-hidden /><span>{count} {count === 1 ? 'model' : 'models'}</span></div><div className="card-body"><h2>{c.name}</h2><p>{c.description || 'No description'}</p></div></a>; })}</div>
  </section>;
}

function CollectionDetail({ slug }: { slug: string }) {
  const qc = useQueryClient();
  const { data } = useQuery<Collection>({ queryKey: ['collection', slug], queryFn: () => api(`/api/collections/${slug}`) });
  const shares = useQuery<Share[]>({ queryKey: ['shares'], queryFn: () => api('/api/shares') });
  const invalidate = () => { qc.invalidateQueries({ queryKey: ['collection', slug] }); qc.invalidateQueries({ queryKey: ['collections'] }); };
  const patch = useMutation<Collection, Error, Partial<Collection>>({ mutationFn: (body) => api(`/api/collections/${data?.id}`, { method: 'PATCH', body: JSON.stringify(body) }), onSuccess: (updated) => { qc.setQueryData(['collection', slug], updated); qc.invalidateQueries({ queryKey: ['collections'] }); if (updated.slug !== slug) navigate(`/collections/${updated.slug}`, true); } });
  const reorder = useMutation({ mutationFn: (modelIds: string[]) => api(`/api/collections/${data?.id}/order`, { method: 'PUT', body: JSON.stringify({ modelIds }) }), onSuccess: invalidate });
  const removeMember = useMutation({ mutationFn: (modelID: string) => api(`/api/collections/${data?.id}/models/${modelID}`, { method: 'DELETE' }), onSuccess: invalidate });
  const remove = useMutation({ mutationFn: () => api(`/api/collections/${data?.id}`, { method: 'DELETE' }), onSuccess: () => navigate('/collections') });
  const share = useMutation({ mutationFn: (body: { label: string; expiresAt: number }) => api('/api/shares', { method: 'POST', body: JSON.stringify({ scope: 'collection', targetId: data?.id, ...body }) }), onSuccess: () => qc.invalidateQueries({ queryKey: ['shares'] }) });
  if (!data) return <section className="content"><Empty text="Loading collection" /></section>;
  const move = (index: number, delta: number) => {
    const modelIds = [...(data.modelIds ?? data.models?.map((model) => model.id) ?? [])];
    const target = index + delta;
    if (target < 0 || target >= modelIds.length) return;
    [modelIds[index], modelIds[target]] = [modelIds[target], modelIds[index]];
    reorder.mutate(modelIds);
  };
  return <section className="content">
    <CollectionForm collection={data} onSave={(body) => patch.mutate(body)} />
    <div className="toolbar"><ShareForm onCreate={(body) => share.mutate(body)} /><button type="button" className="danger" onClick={() => remove.mutate()}><Trash2 size={16} />Delete collection</button></div>
    {shares.data?.filter((s) => s.scope === 'collection' && s.targetId === data.id && !s.revokedAt).map((s) => <ShareRow key={s.id} share={s} />)}
    <div className="collection-models">{data.models?.map((model, index) => <div className="collection-model" key={model.id}><ModelCard model={model} /><div className="collection-actions"><button type="button" aria-label={`Move ${model.title} up`} disabled={index === 0} onClick={() => move(index, -1)}>↑</button><button type="button" aria-label={`Move ${model.title} down`} disabled={index === (data.models?.length ?? 0) - 1} onClick={() => move(index, 1)}>↓</button><button type="button" className="danger" aria-label={`Remove ${model.title} from collection`} onClick={() => removeMember.mutate(model.id)}><Trash2 size={16} /></button></div></div>)}</div>
  </section>;
}

function PublicPage({ token }: { token: string }) {
  const selected = new URLSearchParams(window.location.search).get('model');
  const { data, isError } = useQuery<{ model?: Model; collection?: Collection }>({ queryKey: ['public', token], queryFn: () => api(`/api/public/${token}`) });
  if (isError) return <Shell><Empty text="Share not available" /></Shell>;
  const model = data?.model ?? data?.collection?.models?.find((m) => m.id === selected) ?? data?.collection?.models?.[0];
  return <Shell><section className="detail public">{data?.collection && <div className="collection-strip"><strong>{data.collection.name}</strong>{data.collection.models?.map((m) => <a key={m.id} className={m.id === model?.id ? 'active' : ''} href={`/s/${token}?model=${m.id}`}>{m.title}</a>)}</div>}{model ? <PublicModel model={model} token={token} /> : <Empty text="Loading share" />}</section></Shell>;
}

function PublicModel({ model, token }: { model: Model; token: string }) {
  const [selectedFileID, setSelectedFileID] = useState(model.files[0]?.id ?? '');
  const [forceViewer, setForceViewer] = useState(false);
  const file = model.files.find((f) => f.id === selectedFileID) ?? model.files[0];
  const canAutoLoad = !!file && file.sizeBytes <= VIEWER_LIMIT;
  return <><div className="viewer">{file && (canAutoLoad || forceViewer) ? <Suspense fallback={<Empty text="Loading view" />}><ModelViewer file={file} url={`/api/public/${token}/mesh/${file.id}`} /></Suspense> : <div className="static-thumb">{model.primaryThumb ? <img src={`/api/public/${token}/thumbs/${model.primaryThumb}?model=${model.id}`} alt={`${model.title} thumbnail`} /> : <Box size={64} aria-hidden />}{file && <button type="button" onClick={() => setForceViewer(true)}>Load 3D view</button>}</div>}</div><aside className="panel"><h1>{model.title}</h1><Markdown text={model.description} /><h2>Variants and downloads</h2><Select label="Variant" value={file?.id ?? ''} onChange={(v) => { setSelectedFileID(v); setForceViewer(false); }} options={model.files.map((f) => [f.id, f.filename] as [string, string])} />{model.images?.map((img) => <img className="wide-image" key={img.id} src={`/api/public/${token}/images/${img.id}`} alt={`${model.title} image`} />)}{model.files.map((f) => <a className="file" key={f.id} href={`/api/public/${token}/files/${f.id}`}><Download size={16} />{f.filename}<span>{formatBytes(f.sizeBytes)}</span></a>)}</aside></>;
}

function CollectionMembership({ collections, model }: { collections: Collection[]; model: Model }) {
  const qc = useQueryClient();
  const mutate = useMutation({ mutationFn: ({ collectionID, has }: { collectionID: string; has: boolean }) => api(`/api/collections/${collectionID}/models/${model.id}`, { method: has ? 'DELETE' : 'PUT' }), onSuccess: () => qc.invalidateQueries({ queryKey: ['collections'] }) });
  return <div className="checks">{collections.map((c) => { const has = c.modelIds?.includes(model.id) ?? false; return <label key={c.id}><input type="checkbox" checked={has} onChange={() => mutate.mutate({ collectionID: c.id, has })} />{c.name}</label>; })}</div>;
}

function CollectionForm({ collection, onSave }: { collection?: Collection; onSave: (body: Partial<Collection>) => void }) {
  const [name, setName] = useState(collection?.name ?? '');
  const [description, setDescription] = useState(collection?.description ?? '');
  const [coverModelId, setCoverModelId] = useState(collection?.coverModelId ?? '');
  useEffect(() => { setName(collection?.name ?? ''); setDescription(collection?.description ?? ''); setCoverModelId(collection?.coverModelId ?? ''); }, [collection?.id, collection?.name, collection?.description, collection?.coverModelId]);
  return <form className="stack inline-form" onSubmit={(e) => { e.preventDefault(); onSave({ name, description, coverModelId }); }}><label>{collection ? 'Collection name' : 'Name'}<input value={name} onChange={(e) => setName(e.target.value)} /></label><label>{collection ? 'Collection description' : 'Description'}<input value={description} onChange={(e) => setDescription(e.target.value)} /></label>{collection && <label>Cover model<select value={coverModelId} onChange={(e) => setCoverModelId(e.target.value)}><option value="">Automatic</option>{collection.models?.map((model) => <option key={model.id} value={model.id}>{model.title}</option>)}</select></label>}<button type="submit"><Plus size={16} />{collection ? 'Save collection' : 'Create collection'}</button></form>;
}

function ShareForm({ onCreate }: { onCreate: (body: { label: string; expiresAt: number }) => void }) {
  const [label, setLabel] = useState('');
  const [days, setDays] = useState('30');
  return <form className="inline-form" onSubmit={(e) => { e.preventDefault(); onCreate({ label, expiresAt: Math.floor(Date.now() / 1000) + Number(days || 30) * 86400 }); }}><label>Label<input value={label} onChange={(e) => setLabel(e.target.value)} /></label><label>Days<input type="number" min="1" value={days} onChange={(e) => setDays(e.target.value)} /></label><button type="submit"><Link2 size={16} />Create share</button></form>;
}

function ShareRow({ share }: { share: Share }) {
  return <div className="file"><a href={`/s/${share.token}`}><Link2 size={16} />/s/{share.token}</a><small>{share.expiresAt ? new Date(share.expiresAt * 1000).toLocaleDateString() : 'No expiry'}</small></div>;
}

function UploadInline({ label, path, onDone }: { label: string; path: string; onDone: (value: unknown) => void }) {
  const [file, setFile] = useState<File | null>(null);
  const input = useRef<HTMLInputElement>(null);
  const mutation = useMutation({ mutationFn: async () => { if (!file) return null; const fd = new FormData(); fd.append('file', file); return api(path, { method: 'POST', body: fd }); }, onSuccess: (value) => { setFile(null); if (input.current) input.current.value = ''; onDone(value); } });
  return <form className="upload compact-upload" onSubmit={(e) => { e.preventDefault(); mutation.mutate(); }}><label className="compact-picker"><input ref={input} className="visually-hidden" type="file" onChange={(e) => setFile(e.target.files?.[0] ?? null)} /><span><Plus size={16} /><span title={file?.name}>{file?.name || label}</span></span></label><button type="submit" disabled={!file || mutation.isPending}><Upload size={17} />{mutation.isPending ? 'Uploading' : 'Upload'}</button>{mutation.isError && <p className="error">Upload failed</p>}</form>;
}

function LazyImage({ src, alt }: { src: string; alt: string }) {
  const ref = useRef<HTMLImageElement>(null);
  useEffect(() => {
    const img = ref.current;
    if (!img) return;
    const observer = new IntersectionObserver(([entry]) => { if (entry.isIntersecting) { img.src = src; observer.disconnect(); } });
    observer.observe(img);
    return () => observer.disconnect();
  }, [src]);
  return <img ref={ref} alt={alt} loading="lazy" />;
}

function Markdown({ text }: { text: string }) {
  if (!text) return null;
  return <div className="markdown"><ReactMarkdown>{text}</ReactMarkdown></div>;
}

function PageHeader({ eyebrow, title, description, action }: { eyebrow: string; title: string; description: string; action?: ReactNode }) {
  return <header className="page-header"><div><span className="eyebrow">{eyebrow}</span><h1>{title}</h1><p>{description}</p></div>{action}</header>;
}

function SectionHeading({ icon, title, description }: { icon: ReactNode; title: string; description: string }) {
  return <header className="section-heading"><span className="surface-icon">{icon}</span><div><h2>{title}</h2><p>{description}</p></div></header>;
}

function VariantPicker({ files, selectedFileID, onSelect, thumbnailURL }: { files: ModelFile[]; selectedFileID: string; onSelect: (fileID: string) => void; thumbnailURL: (file: ModelFile) => string }) {
  const [open, setOpen] = useState(false);
  const picker = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const options = useRef<Array<HTMLButtonElement | null>>([]);
  const selected = files.find((file) => file.id === selectedFileID) ?? files[0];
  const selectedIndex = Math.max(0, files.findIndex((file) => file.id === selected?.id));
  useEffect(() => {
    if (!open) return undefined;
    const focus = window.setTimeout(() => options.current[selectedIndex]?.focus(), 0);
    const close = (event: PointerEvent) => { if (!picker.current?.contains(event.target as Node)) setOpen(false); };
    document.addEventListener('pointerdown', close);
    return () => { window.clearTimeout(focus); document.removeEventListener('pointerdown', close); };
  }, [open, selectedIndex]);
  if (!selected) return null;
  const closeAndFocusTrigger = () => { setOpen(false); trigger.current?.focus(); };
  const moveFocus = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    const current = options.current.findIndex((option) => option === document.activeElement);
    let next = current;
    if (event.key === 'ArrowDown') next = (current + 1) % files.length;
    else if (event.key === 'ArrowUp') next = (current - 1 + files.length) % files.length;
    else if (event.key === 'Home') next = 0;
    else if (event.key === 'End') next = files.length - 1;
    else if (event.key === 'Escape') { event.preventDefault(); closeAndFocusTrigger(); return; }
    else return;
    event.preventDefault();
    options.current[next]?.focus();
  };
  return <div className={`variant-picker${open ? ' open' : ''}`} ref={picker} onBlur={(event) => { if (!event.currentTarget.contains(event.relatedTarget as Node)) setOpen(false); }}>
    <span className="variant-picker-label">Variation</span>
    <button ref={trigger} type="button" className="variant-picker-trigger" aria-label={`Choose variant, currently ${selected.filename}`} aria-haspopup="menu" aria-expanded={open} onClick={() => setOpen((value) => !value)}>
      <VariantPreview file={selected} src={thumbnailURL(selected)} />
      <ChevronDown size={18} aria-hidden />
    </button>
    {open && <div className="variant-menu" role="menu" aria-label="Variants" onKeyDown={moveFocus}>
      {files.map((file, index) => <button ref={(option) => { options.current[index] = option; }} type="button" role="menuitemradio" aria-checked={file.id === selected.id} className={file.id === selected.id ? 'selected' : ''} key={file.id} onClick={() => { onSelect(file.id); closeAndFocusTrigger(); }}>
        <VariantPreview file={file} src={thumbnailURL(file)} />
        {file.id === selected.id && <Check size={17} aria-hidden />}
      </button>)}
    </div>}
  </div>;
}

function VariantPreview({ file, src }: { file: ModelFile; src: string }) {
  return <><span className="variant-preview">{src ? <img src={src} alt="" loading="lazy" /> : <Box size={22} aria-hidden />}</span><span className="variant-copy"><strong>{file.filename}</strong><small>{file.format.toUpperCase()} · {formatBytes(file.sizeBytes)}</small></span></>;
}

function EmptyState({ icon, title, text, compact = false }: { icon: ReactNode; title: string; text: string; compact?: boolean }) {
  return <div className={`empty-state${compact ? ' compact' : ''}`}><span className="empty-state-icon">{icon}</span><h2>{title}</h2><p>{text}</p></div>;
}

function Select({ label, value, onChange, options, compact = false }: { label: string; value: string; onChange: (value: string) => void; options: [string, string][]; compact?: boolean }) {
  return <label className={`select${compact ? ' compact-select' : ''}`}><span className={compact ? 'visually-hidden' : undefined}>{label}</span><select aria-label={compact ? label : undefined} value={value} onChange={(e) => onChange(e.target.value)}>{options.map(([v, text]) => <option value={v} key={v}>{text}</option>)}</select></label>;
}

function fileThumbName(file?: ModelFile) {
  return file?.thumbPath?.split('/').pop() ?? '';
}

function Empty({ text }: { text: string }) {
  return <div className="empty">{text}</div>;
}

function useDebounced(value: string, ms: number) {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => { const t = window.setTimeout(() => setDebounced(value), ms); return () => window.clearTimeout(t); }, [value, ms]);
  return debounced;
}

async function api(path: string, init: RequestInit = {}) {
  const headers = init.body instanceof FormData ? init.headers : { 'Content-Type': 'application/json', ...init.headers };
  const res = await fetch(path, { credentials: 'include', ...init, headers });
  const body = await res.text();
  if (!res.ok) throw new Error(body);
  return body ? JSON.parse(body) : null;
}

function navigate(path: string, replace = false) {
  if (replace) window.history.replaceState({}, '', path);
  else window.history.pushState({}, '', path);
  window.dispatchEvent(new Event(NAVIGATION_EVENT));
}

function dims(f: ModelFile) {
  return `${f.bboxX.toFixed(1)} x ${f.bboxY.toFixed(1)} x ${f.bboxZ.toFixed(1)}`;
}

function formatBytes(n: number) {
  if (!n) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB'];
  const i = Math.min(Math.floor(Math.log(n) / Math.log(1024)), units.length - 1);
  return `${(n / 1024 ** i).toFixed(i ? 1 : 0)} ${units[i]}`;
}
