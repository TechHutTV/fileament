import { QueryClient, QueryClientProvider, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Box, Check, Download, Link2, Moon, Plus, Search, Settings, Sun, Trash2, Upload, X } from 'lucide-react';
import { Suspense, lazy, useEffect, useRef, useState, type ReactNode } from 'react';

const ModelViewer = lazy(() => import('./Viewer'));
const VIEWER_LIMIT = 50 * 1024 * 1024;
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
type Collection = { id: string; name: string; slug: string; description: string; coverModelId?: string; models?: Model[] };
type Share = { id: string; token: string; scope: 'model' | 'collection'; targetId: string; label?: string; expiresAt?: number; revokedAt?: number };

export function Root() {
  return <QueryClientProvider client={client}><App /></QueryClientProvider>;
}

export function App() {
  const path = window.location.pathname;
  if (path.startsWith('/s/')) return <PublicPage token={path.split('/')[2]} />;
  return <OwnerApp />;
}

function OwnerApp() {
  const [dark, setDark] = useState(localStorage.getItem('fileament-theme') === 'dark');
  useEffect(() => {
    document.documentElement.dataset.theme = dark ? 'dark' : 'light';
    localStorage.setItem('fileament-theme', dark ? 'dark' : 'light');
  }, [dark]);
  const path = window.location.pathname;
  const me = useQuery<Me>({ queryKey: ['me'], queryFn: () => api('/api/me') });
  if (me.isLoading) return <Shell><Empty text="Loading" /></Shell>;
  if (me.data?.setupRequired) return <AuthScreen mode="setup" />;
  if (!me.data?.authenticated) return <AuthScreen mode="login" />;
  return (
    <Shell>
      <nav className="topbar">
        <a className="brand" href="/"><Box size={22} />Fileament</a>
        <div className="navlinks">
          <a href="/upload"><Upload size={18} />Upload</a>
          <a href="/collections">Collections</a>
          <a href="/settings"><Settings size={18} />Settings</a>
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
  const qc = useQueryClient();
  const mutation = useMutation({
    mutationFn: () => api(mode === 'setup' ? '/api/auth/setup' : '/api/auth/login', { method: 'POST', body: JSON.stringify({ password }) }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['me'] }),
  });
  return (
    <main className="auth">
      <form onSubmit={(e) => { e.preventDefault(); mutation.mutate(); }}>
        <h1>{mode === 'setup' ? 'Set owner password' : 'Owner login'}</h1>
        <label>Password<input type="password" minLength={12} value={password} onChange={(e) => setPassword(e.target.value)} autoFocus /></label>
        <button type="submit">{mode === 'setup' ? 'Create owner' : 'Log in'}</button>
        {mutation.isError && <p className="error">Authentication failed</p>}
      </form>
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
    es.onerror = () => es.close();
    return () => es.close();
  }, [qc]);
  return (
    <section className="content">
      <div className="toolbar multi">
        <label className="search"><Search size={18} /><input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search models" aria-label="Search models" /></label>
        <Select label="Tag" value={tag} onChange={setTag} options={[['', 'All tags'], ...tagItems.map((t) => [t.slug, t.name] as [string, string])]} />
        <Select label="Collection" value={collection} onChange={setCollection} options={[['', 'All collections'], ...collectionItems.map((c) => [c.slug, c.name] as [string, string])]} />
        <Select label="Sort" value={sort} onChange={setSort} options={[['created', 'Newest'], ['updated', 'Updated'], ['title', 'Title'], ['size', 'Size']]} />
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
  return (
    <a className="card" href={`/models/${model.id}`}>
      <div className="thumb">{src ? <LazyImage src={src} alt={`${model.title} thumbnail`} /> : <Box size={42} aria-hidden />}</div>
      <h2>{model.title}</h2>
      <p>{formatBytes(model.totalBytes)}</p>
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
  const removeModel = useMutation({ mutationFn: () => api(`/api/models/${id}`, { method: 'DELETE' }), onSuccess: () => { window.location.href = '/'; } });
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
        {model.files.map((f) => <div className="file" key={f.id}><a href={`/files/${model.id}/${f.id}`}><Download size={16} />{f.filename}</a><small>{f.triangleCount} tris · {dims(f)} · {formatBytes(f.sizeBytes)}</small><button type="button" className="icon" title="Use thumbnail" onClick={() => setThumb.mutate(f.id)}><Check size={16} /></button><button type="button" className="icon danger" title="Delete file" onClick={() => deleteFile.mutate(f.id)}><Trash2 size={16} /></button></div>)}
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
  const [done, setDone] = useState('');
  return <section className="content narrow"><UploadInline label="Upload model or ZIP bundle" path="/api/models" onDone={(m) => setDone((m as Model).id)} />{done && <a className="file" href={`/models/${done}`}>Open uploaded model</a>}</section>;
}

function SettingsPage() {
  const qc = useQueryClient();
  const { data } = useQuery<{ totalBytes: number }>({ queryKey: ['storage'], queryFn: () => api('/api/storage') });
  const { data: shares } = useQuery<Share[]>({ queryKey: ['shares'], queryFn: () => api('/api/shares') });
  const revoke = useMutation({ mutationFn: (id: string) => api(`/api/shares/${id}`, { method: 'DELETE' }), onSuccess: () => qc.invalidateQueries({ queryKey: ['shares'] }) });
  const [currentPassword, setCurrentPassword] = useState('');
  const [newPassword, setNewPassword] = useState('');
  const change = useMutation({ mutationFn: () => api('/api/auth/password', { method: 'POST', body: JSON.stringify({ currentPassword, newPassword }) }), onSuccess: () => { setCurrentPassword(''); setNewPassword(''); } });
  return <section className="content narrow"><h1>Settings</h1><p>Storage: {formatBytes(data?.totalBytes ?? 0)}</p><form className="stack" onSubmit={(e) => { e.preventDefault(); change.mutate(); }}><label>Current password<input type="password" value={currentPassword} onChange={(e) => setCurrentPassword(e.target.value)} /></label><label>New password<input type="password" minLength={12} value={newPassword} onChange={(e) => setNewPassword(e.target.value)} /></label><button type="submit">Change password</button>{change.isError && <p className="error">Password change failed</p>}</form><h2>Share links</h2>{shares?.map((s) => <div className="file" key={s.id}><a href={`/s/${s.token}`}><Link2 size={16} />{s.label || s.scope}</a><small>{s.revokedAt ? 'Revoked' : s.expiresAt ? new Date(s.expiresAt * 1000).toLocaleDateString() : 'No expiry'}</small><button type="button" className="icon danger" onClick={() => revoke.mutate(s.id)}><Trash2 size={16} /></button></div>)}</section>;
}

function CollectionsPage() {
  const qc = useQueryClient();
  const { data, isLoading } = useQuery<Collection[]>({ queryKey: ['collections'], queryFn: () => api('/api/collections') });
  const create = useMutation({ mutationFn: (body: Partial<Collection>) => api('/api/collections', { method: 'POST', body: JSON.stringify(body) }), onSuccess: () => qc.invalidateQueries({ queryKey: ['collections'] }) });
  return <section className="content"><CollectionForm onSave={(body) => create.mutate(body)} />{isLoading && <Empty text="Loading collections" />}<div className="grid">{(data ?? []).map((c) => <a className="card" href={`/collections/${c.slug}`} key={c.id}><div className="thumb"><Box size={42} aria-hidden /></div><h2>{c.name}</h2><p>{c.description}</p></a>)}</div></section>;
}

function CollectionDetail({ slug }: { slug: string }) {
  const qc = useQueryClient();
  const { data } = useQuery<Collection>({ queryKey: ['collection', slug], queryFn: () => api(`/api/collections/${slug}`) });
  const shares = useQuery<Share[]>({ queryKey: ['shares'], queryFn: () => api('/api/shares') });
  const patch = useMutation({ mutationFn: (body: Partial<Collection>) => api(`/api/collections/${data?.id}`, { method: 'PATCH', body: JSON.stringify(body) }), onSuccess: () => qc.invalidateQueries({ queryKey: ['collection', slug] }) });
  const remove = useMutation({ mutationFn: () => api(`/api/collections/${data?.id}`, { method: 'DELETE' }), onSuccess: () => { window.location.href = '/collections'; } });
  const share = useMutation({ mutationFn: (body: { label: string; expiresAt: number }) => api('/api/shares', { method: 'POST', body: JSON.stringify({ scope: 'collection', targetId: data?.id, ...body }) }), onSuccess: () => qc.invalidateQueries({ queryKey: ['shares'] }) });
  if (!data) return <section className="content"><Empty text="Loading collection" /></section>;
  return <section className="content"><CollectionForm collection={data} onSave={(body) => patch.mutate(body)} /><div className="toolbar"><ShareForm onCreate={(body) => share.mutate(body)} /><button type="button" className="danger" onClick={() => remove.mutate()}><Trash2 size={16} />Delete collection</button></div>{shares.data?.filter((s) => s.scope === 'collection' && s.targetId === data.id && !s.revokedAt).map((s) => <ShareRow key={s.id} share={s} />)}<div className="grid">{data.models?.map((model) => <ModelCard model={model} key={model.id} />)}</div></section>;
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
  return <div className="checks">{collections.map((c) => { const has = c.models?.some((m) => m.id === model.id) ?? false; return <label key={c.id}><input type="checkbox" checked={has} onChange={() => mutate.mutate({ collectionID: c.id, has })} />{c.name}</label>; })}</div>;
}

function CollectionForm({ collection, onSave }: { collection?: Collection; onSave: (body: Partial<Collection>) => void }) {
  const [name, setName] = useState(collection?.name ?? '');
  const [description, setDescription] = useState(collection?.description ?? '');
  return <form className="stack inline-form" onSubmit={(e) => { e.preventDefault(); onSave({ name, description }); }}><label>Name<input value={name} onChange={(e) => setName(e.target.value)} /></label><label>Description<input value={description} onChange={(e) => setDescription(e.target.value)} /></label><button type="submit"><Plus size={16} />{collection ? 'Save collection' : 'Create collection'}</button></form>;
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
  const mutation = useMutation({ mutationFn: async () => { if (!file) return null; const fd = new FormData(); fd.append('file', file); return api(path, { method: 'POST', body: fd }); }, onSuccess: (value) => { setFile(null); onDone(value); } });
  return <form className="upload" onSubmit={(e) => { e.preventDefault(); mutation.mutate(); }}><label>{label}<input type="file" onChange={(e) => setFile(e.target.files?.[0] ?? null)} /></label><button type="submit"><Upload size={18} />Upload</button>{mutation.isError && <p className="error">Upload failed</p>}</form>;
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
  return <div className="markdown">{text.split(/\n{2,}/).map((block, i) => <p key={i}>{block.split('\n').map((line, j) => <span key={j}>{line}{j < block.split('\n').length - 1 && <br />}</span>)}</p>)}</div>;
}

function Select({ label, value, onChange, options }: { label: string; value: string; onChange: (value: string) => void; options: [string, string][] }) {
  return <label className="select">{label}<select value={value} onChange={(e) => onChange(e.target.value)}>{options.map(([v, text]) => <option value={v} key={v}>{text}</option>)}</select></label>;
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
  if (!res.ok) throw new Error(await res.text());
  if (res.status === 204) return null;
  return res.json();
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
