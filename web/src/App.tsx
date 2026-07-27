import { QueryClient, QueryClientProvider, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Box, Check, Download, Eye, EyeOff, Folder, HardDrive, Link2, Lock, Moon, Plus, Search, Settings, Sun, Trash2, Upload, X } from 'lucide-react';
import { Suspense, lazy, useEffect, useRef, useState, type ReactNode } from 'react';
import ReactMarkdown from 'react-markdown';

const ModelViewer = lazy(() => import('./Viewer'));
const VIEWER_LIMIT = 50 * 1024 * 1024;
const NAVIGATION_EVENT = 'fileament:navigate';
const client = new QueryClient();

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
type UploadItem = { key: string; file: File; status: UploadStatus; model?: Model; error?: string };

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
  if (me.data?.setupRequired) return <AuthScreen mode="setup" />;
  if (!me.data?.authenticated) return <AuthScreen mode="login" />;
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
  const [cursor, setCursor] = useState('');
  const [items, setItems] = useState<Model[]>([]);
  const debounced = useDebounced(q, 250);
  const qc = useQueryClient();
  const collections = useQuery<Collection[]>({ queryKey: ['collections'], queryFn: () => api('/api/collections') });
  const tags = useQuery<{ name: string; slug: string }[]>({ queryKey: ['tags'], queryFn: () => api('/api/tags') });
  const collectionItems = Array.isArray(collections.data) ? collections.data : [];
  const tagItems = Array.isArray(tags.data) ? tags.data : [];
  const query = new URLSearchParams({ limit: '24', sort });
  if (debounced) query.set('q', debounced);
  if (tag) query.set('tag', tag);
  if (collection) query.set('collection', collection);
  if (cursor) query.set('cursor', cursor);
  const page = useQuery<Page>({ queryKey: ['models', debounced, tag, collection, sort, cursor], queryFn: () => api(`/api/models?${query}`) });
  useEffect(() => { setCursor(''); setItems([]); }, [debounced, tag, collection, sort]);
  useEffect(() => {
    if (!page.data) return;
    setItems((prev) => cursor ? [...prev, ...page.data.items] : page.data.items);
  }, [page.data, cursor]);
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
      {page.data?.nextCursor && <button type="button" className="load" onClick={() => setCursor(page.data.nextCursor)}>Load more</button>}
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
        {file && (canAutoLoad || forceViewer) ? <Suspense fallback={<Empty text="Loading view" />}><ModelViewer file={file} url={`/mesh/${model.id}/${file.id}`} /></Suspense> : <div className="static-thumb">{model.primaryThumb ? <img src={`/thumbs/${model.id}/${model.primaryThumb}`} alt={`${model.title} thumbnail`} /> : <Box size={64} aria-hidden />}{file && <button type="button" onClick={() => setForceViewer(true)}>Load 3D view</button>}</div>}
      </div>
      <aside className="panel">
        <ModelEditor model={model} onSave={(body) => patch.mutate(body)} />
        <Markdown text={model.description} />
        <div className="meta">{model.author && <span>By {model.author}</span>}{model.license && <span>{model.license}</span>}{model.sourceUrl && <a href={model.sourceUrl}>Source</a>}</div>
        <div className="tags">{model.tags?.map((t) => <span key={t}>{t}</span>)}</div>
        <h2>Files</h2>
        <Select label="Viewer file" value={file?.id ?? ''} onChange={(v) => { setSelectedFileID(v); setForceViewer(false); }} options={model.files.map((f) => [f.id, f.filename] as [string, string])} />
        {model.files.map((f) => <div className="file model-file" key={f.id}><div className="model-file-copy"><a href={`/files/${model.id}/${f.id}`}><Download size={16} /><span>{f.filename}</span></a><small>{f.triangleCount} tris · {dims(f)} · {formatBytes(f.sizeBytes)}</small></div><div className="model-file-actions"><button type="button" className="icon" title="Use thumbnail" onClick={() => setThumb.mutate(f.id)}><Check size={16} /></button><button type="button" className="icon danger" title="Delete file" onClick={() => deleteFile.mutate(f.id)}><Trash2 size={16} /></button></div></div>)}
        <UploadInline label="Add files or ZIP" path={`/api/models/${id}/files`} onDone={invalidate} />
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
  const sequence = useRef(0);
  const input = useRef<HTMLInputElement>(null);
  const controllers = useRef(new Map<string, AbortController>());
  const cancelled = useRef(new Set<string>());
  const uploadChain = useRef<Promise<void>>(Promise.resolve());

  const updateItem = (key: string, update: Partial<UploadItem>) => {
    setItems((current) => current.map((item) => item.key === key ? { ...item, ...update } : item));
  };

  const uploadItem = async (item: UploadItem) => {
    const controller = new AbortController();
    controllers.current.set(item.key, controller);
    updateItem(item.key, { status: 'uploading' });
    const body = new FormData();
    body.append('file', item.file);
    try {
      const model = await api('/api/models', { method: 'POST', body, signal: controller.signal }) as Model;
      if (cancelled.current.has(item.key)) {
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
        return;
      }
      updateItem(item.key, { model, status: model.primaryThumb ? 'ready' : 'processing' });
      qc.invalidateQueries({ queryKey: ['models'] });
      qc.invalidateQueries({ queryKey: ['storage'] });
    } catch (error) {
      if (!cancelled.current.has(item.key) && (error as Error).name !== 'AbortError') {
        updateItem(item.key, { status: 'error', error: 'Upload failed. Remove it and try again.' });
      }
    } finally {
      controllers.current.delete(item.key);
      cancelled.current.delete(item.key);
    }
  };

  const addFiles = (files: FileList | File[]) => {
    const queued = Array.from(files).map((file) => ({ key: `upload-${++sequence.current}`, file, status: 'queued' as const }));
    if (queued.length === 0) return;
    setItems((current) => [...current, ...queued]);
    queued.forEach((item) => {
      uploadChain.current = uploadChain.current.then(async () => {
        if (cancelled.current.has(item.key)) {
          cancelled.current.delete(item.key);
          return;
        }
        await uploadItem(item);
      });
    });
  };

  const removeItem = async (item: UploadItem) => {
    cancelled.current.add(item.key);
    controllers.current.get(item.key)?.abort();
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

  useEffect(() => () => { controllers.current.forEach((controller) => controller.abort()); }, []);

  const active = items.some((item) => item.status === 'queued' || item.status === 'uploading' || item.status === 'removing');
  const completed = items.filter((item) => item.status === 'ready' || item.status === 'processing').length;
  return <section className="upload-page">
    <header className="upload-header">
      <span className="eyebrow">Add models</span>
      <h1>Build your library</h1>
      <p>Drop individual models or ZIP bundles. Each file uploads automatically and becomes its own catalog entry.</p>
    </header>
    <div
      className={`upload-dropzone${dragging ? ' dragging' : ''}`}
      role="button"
      tabIndex={0}
      aria-label="Drop 3D files or choose files"
      onClick={() => input.current?.click()}
      onKeyDown={(event) => { if (event.key === 'Enter' || event.key === ' ') { event.preventDefault(); input.current?.click(); } }}
      onDragEnter={(event) => { event.preventDefault(); setDragging(true); }}
      onDragOver={(event) => { event.preventDefault(); setDragging(true); }}
      onDragLeave={(event) => { if (!event.currentTarget.contains(event.relatedTarget as Node)) setDragging(false); }}
      onDrop={(event) => { event.preventDefault(); setDragging(false); addFiles(event.dataTransfer.files); }}
    >
      <input ref={input} className="visually-hidden" type="file" multiple accept=".stl,.obj,.3mf,.zip" aria-label="Choose 3D files" onChange={(event) => { if (event.target.files) addFiles(event.target.files); event.target.value = ''; }} />
      <span className="dropzone-icon"><Upload size={28} /></span>
      <strong>{dragging ? 'Drop to start uploading' : 'Drop 3D files here'}</strong>
      <span>or click to browse your files</span>
      <small>STL, OBJ, 3MF, and ZIP bundles</small>
    </div>
    {items.length > 0 && <section className="upload-queue" aria-live="polite">
      <div className="queue-heading"><div><span className="eyebrow">Upload queue</span><h2>{items.length} {items.length === 1 ? 'model' : 'models'}</h2></div><span>{completed} uploaded</span></div>
      <div className="upload-grid">{items.map((item) => {
        const thumbnail = item.model?.primaryThumb ? `/thumbs/${item.model.id}/${item.model.primaryThumb}` : '';
        const label = item.status === 'queued' ? 'Queued' : item.status === 'uploading' ? 'Uploading' : item.status === 'processing' ? 'Generating preview' : item.status === 'ready' ? 'Ready' : item.status === 'removing' ? 'Removing' : 'Needs attention';
        const placeholder = item.status === 'queued' ? 'Waiting to upload' : item.status === 'uploading' ? 'Uploading model' : item.status === 'error' ? 'Upload failed' : item.status === 'removing' ? 'Removing model' : 'Rendering preview';
        return <article className={`upload-card ${item.status}`} key={item.key}>
          <div className="upload-preview">{thumbnail ? <img src={thumbnail} alt={`${item.file.name} thumbnail`} /> : <div className="upload-placeholder"><Box size={38} /><span>{placeholder}</span></div>}{item.status === 'uploading' && <span className="upload-progress" />}</div>
          <button type="button" className="icon upload-remove" aria-label={`${item.model ? 'Remove' : 'Cancel'} ${item.file.name}${item.model ? '' : ' upload'}`} onClick={() => { void removeItem(item); }} disabled={item.status === 'removing'}><X size={17} /></button>
          <div className="upload-card-body"><div><h3>{item.model?.title || item.file.name}</h3><p>{item.file.name} · {formatBytes(item.file.size)}</p></div><span className={`upload-status ${item.status}`}>{item.status === 'ready' && <Check size={13} />}{label}</span>{item.error && <p className="upload-error">{item.error}</p>}</div>
        </article>;
      })}</div>
    </section>}
    {items.length > 0 && <footer className="upload-finish"><div><strong>{active ? 'Uploads in progress' : `${completed} ${completed === 1 ? 'model' : 'models'} added`}</strong><span>{active ? 'You can keep adding files while these finish.' : 'Everything is saved to your library.'}</span></div><button type="button" disabled={active} onClick={() => navigate('/')}><Check size={18} />Finish and view library</button></footer>}
  </section>;
}

function SettingsPage() {
  const qc = useQueryClient();
  const { data } = useQuery<{ totalBytes: number }>({ queryKey: ['storage'], queryFn: () => api('/api/storage') });
  const { data: shares, isLoading: sharesLoading } = useQuery<Share[]>({ queryKey: ['shares'], queryFn: () => api('/api/shares') });
  const revoke = useMutation({ mutationFn: (id: string) => api(`/api/shares/${id}`, { method: 'DELETE' }), onSuccess: () => qc.invalidateQueries({ queryKey: ['shares'] }) });
  const [currentPassword, setCurrentPassword] = useState('');
  const [newPassword, setNewPassword] = useState('');
  const change = useMutation({ mutationFn: () => api('/api/auth/password', { method: 'POST', body: JSON.stringify({ currentPassword, newPassword }) }), onSuccess: () => { setCurrentPassword(''); setNewPassword(''); } });
  return <section className="content narrow settings-page">
    <PageHeader eyebrow="Owner controls" title="Settings" description="Manage storage, account security, and public access to your library." />
    <div className="storage-card"><span className="surface-icon"><HardDrive size={20} /></span><div><span>Library storage</span><strong>{formatBytes(data?.totalBytes ?? 0)}</strong></div></div>
    <section className="surface-card settings-card">
      <SectionHeading icon={<Lock size={19} />} title="Security" description="Update the password used to access this private library." />
      <form className="stack settings-form" onSubmit={(e) => { e.preventDefault(); change.mutate(); }}><label>Current password<input type="password" value={currentPassword} onChange={(e) => setCurrentPassword(e.target.value)} /></label><label>New password<input type="password" minLength={12} value={newPassword} onChange={(e) => setNewPassword(e.target.value)} /></label><small className="field-help">Use at least 12 characters.</small><button type="submit" disabled={change.isPending}>{change.isPending ? 'Updating password' : 'Change password'}</button>{change.isError && <p className="error">Password change failed</p>}{change.isSuccess && <p className="success">Password updated.</p>}</form>
    </section>
    <section className="surface-card settings-card">
      <SectionHeading icon={<Link2 size={19} />} title="Share links" description="Review and revoke public links created for models and collections." />
      {sharesLoading && <Empty text="Loading share links" />}
      {!sharesLoading && (shares?.length ?? 0) === 0 && <EmptyState icon={<Link2 size={24} />} title="No share links yet" text="Links you create from a model or collection will appear here." compact />}
      {shares?.map((s) => <div className="file share-row" key={s.id}><a href={`/s/${s.token}`}><Link2 size={16} />{s.label || s.scope}</a><small>{s.revokedAt ? 'Revoked' : s.expiresAt ? new Date(s.expiresAt * 1000).toLocaleDateString() : 'No expiry'}</small><button type="button" className="icon danger" aria-label={`Revoke ${s.label || s.scope} share`} onClick={() => revoke.mutate(s.id)}><Trash2 size={16} /></button></div>)}
    </section>
  </section>;
}

function CollectionsPage() {
  const qc = useQueryClient();
  const [formVersion, setFormVersion] = useState(0);
  const { data, isLoading } = useQuery<Collection[]>({ queryKey: ['collections'], queryFn: () => api('/api/collections') });
  const create = useMutation({ mutationFn: (body: Partial<Collection>) => api('/api/collections', { method: 'POST', body: JSON.stringify(body) }), onSuccess: () => { setFormVersion((version) => version + 1); qc.invalidateQueries({ queryKey: ['collections'] }); } });
  return <section className="content page-content collections-page">
    <PageHeader eyebrow="Organize your library" title="Collections" description="Group related models into focused sets for projects, printers, or workflows." />
    <section className="surface-card collection-create">
      <SectionHeading icon={<Folder size={19} />} title="Create a collection" description="Start a new group and add models from their detail pages." />
      <CollectionForm key={formVersion} onSave={(body) => create.mutate(body)} />
    </section>
    {isLoading && <Empty text="Loading collections" />}
    {!isLoading && (data?.length ?? 0) === 0 && <EmptyState icon={<Folder size={28} />} title="No collections yet" text="Create your first collection above, then add models from your library." />}
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
  return <><div className="viewer">{file && (canAutoLoad || forceViewer) ? <Suspense fallback={<Empty text="Loading view" />}><ModelViewer file={file} url={`/api/public/${token}/mesh/${file.id}`} /></Suspense> : <div className="static-thumb">{model.primaryThumb ? <img src={`/api/public/${token}/thumbs/${model.primaryThumb}?model=${model.id}`} alt={`${model.title} thumbnail`} /> : <Box size={64} aria-hidden />}{file && <button type="button" onClick={() => setForceViewer(true)}>Load 3D view</button>}</div>}</div><aside className="panel"><h1>{model.title}</h1><Markdown text={model.description} /><Select label="Viewer file" value={file?.id ?? ''} onChange={(v) => { setSelectedFileID(v); setForceViewer(false); }} options={model.files.map((f) => [f.id, f.filename] as [string, string])} />{model.images?.map((img) => <img className="wide-image" key={img.id} src={`/api/public/${token}/images/${img.id}`} alt={`${model.title} image`} />)}{model.files.map((f) => <a className="file" key={f.id} href={`/api/public/${token}/files/${f.id}`}><Download size={16} />{f.filename}<span>{formatBytes(f.sizeBytes)}</span></a>)}</aside></>;
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

function EmptyState({ icon, title, text, compact = false }: { icon: ReactNode; title: string; text: string; compact?: boolean }) {
  return <div className={`empty-state${compact ? ' compact' : ''}`}><span className="empty-state-icon">{icon}</span><h2>{title}</h2><p>{text}</p></div>;
}

function Select({ label, value, onChange, options, compact = false }: { label: string; value: string; onChange: (value: string) => void; options: [string, string][]; compact?: boolean }) {
  return <label className={`select${compact ? ' compact-select' : ''}`}><span className={compact ? 'visually-hidden' : undefined}>{label}</span><select aria-label={compact ? label : undefined} value={value} onChange={(e) => onChange(e.target.value)}>{options.map(([v, text]) => <option value={v} key={v}>{text}</option>)}</select></label>;
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
