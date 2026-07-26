import { QueryClient, QueryClientProvider, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Box, Download, Moon, Search, Settings, Sun, Upload } from 'lucide-react';
import { FormEvent, Suspense, lazy, useEffect, useRef, useState } from 'react';

const ModelViewer = lazy(() => import('./Viewer'));
const VIEWER_LIMIT = 50 * 1024 * 1024;
const client = new QueryClient();

export type ModelFile = {
  id: string;
  modelId: string;
  filename: string;
  format: 'stl' | 'obj' | '3mf';
  sizeBytes: number;
  triangleCount: number;
  bboxX: number;
  bboxY: number;
  bboxZ: number;
  thumbPath?: string;
};

export type Model = {
  id: string;
  title: string;
  description: string;
  primaryThumb?: string;
  totalBytes: number;
  files: ModelFile[];
  tags?: string[];
};

type Page = { items: Model[]; nextCursor: string };
type Me = { authenticated: boolean; setupRequired: boolean };

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
  if (me.isLoading) return <main className="app"><div className="empty">Loading</div></main>;
  if (me.data?.setupRequired) return <AuthScreen mode="setup" />;
  if (!me.data?.authenticated) return <AuthScreen mode="login" />;
  return (
    <main className="app">
      <nav className="topbar">
        <a className="brand" href="/"><Box size={22} />Fileament</a>
        <div className="navlinks">
          <a href="/upload"><Upload size={18} />Upload</a>
          <a href="/collections">Collections</a>
          <a href="/settings"><Settings size={18} />Settings</a>
          <button className="icon" onClick={() => setDark(!dark)} title="Toggle dark mode">{dark ? <Sun /> : <Moon />}</button>
        </div>
      </nav>
      {path.startsWith('/models/') ? <Detail id={path.split('/')[2]} /> : path.startsWith('/collections/') ? <CollectionDetail slug={path.split('/')[2]} /> : path === '/collections' ? <CollectionsPage /> : path === '/upload' ? <UploadPage /> : path === '/settings' ? <SettingsPage /> : <Catalog />}
    </main>
  );
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
        <input type="password" minLength={12} value={password} onChange={(e) => setPassword(e.target.value)} autoFocus />
        <button>{mode === 'setup' ? 'Create owner' : 'Log in'}</button>
        {mutation.isError && <p className="error">Authentication failed</p>}
      </form>
    </main>
  );
}

function Catalog() {
  const [q, setQ] = useState('');
  const { data } = useQuery<Page>({ queryKey: ['models', q], queryFn: () => api(`/api/models?q=${encodeURIComponent(q)}`) });
  return (
    <section className="content">
      <div className="toolbar">
        <label className="search"><Search size={18} /><input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search models" /></label>
      </div>
      <div className="grid">
        {(data?.items ?? []).map((model) => <ModelCard key={model.id} model={model} />)}
      </div>
    </section>
  );
}

function ModelCard({ model }: { model: Model }) {
  const ref = useRef<HTMLImageElement>(null);
  const src = model.primaryThumb ? `/thumbs/${model.id}/${model.primaryThumb}` : '';
  useEffect(() => {
    const img = ref.current;
    if (!img || !src) return;
    const observer = new IntersectionObserver(([entry]) => {
      if (entry.isIntersecting) {
        img.src = src;
        observer.disconnect();
      }
    });
    observer.observe(img);
    return () => observer.disconnect();
  }, [src]);
  return (
    <a className="card" href={`/models/${model.id}`}>
      <div className="thumb">{src ? <img ref={ref} alt="" /> : <Box size={42} />}</div>
      <h2>{model.title}</h2>
      <p>{formatBytes(model.totalBytes)}</p>
    </a>
  );
}

function Detail({ id }: { id: string }) {
  const { data: model } = useQuery<Model>({ queryKey: ['model', id], queryFn: () => api(`/api/models/${id}`) });
  const [forceViewer, setForceViewer] = useState(false);
  const file = model?.files?.[0];
  const canAutoLoad = !!file && file.sizeBytes <= VIEWER_LIMIT;
  if (!model) return <section className="content"><div className="empty">Loading</div></section>;
  return (
    <section className="detail">
      <div className="viewer">
        {file && (canAutoLoad || forceViewer) ? (
          <Suspense fallback={<div className="empty">Loading view</div>}><ModelViewer file={file} url={`/mesh/${model.id}/${file.id}`} /></Suspense>
        ) : (
          <div className="static-thumb">{model.primaryThumb ? <img src={`/thumbs/${model.id}/${model.primaryThumb}`} alt="" /> : <Box size={64} />}{file && <button onClick={() => setForceViewer(true)}>Load 3D view</button>}</div>
        )}
      </div>
      <aside className="panel">
        <h1>{model.title}</h1>
        <p>{model.description}</p>
        <div className="tags">{model.tags?.map((tag) => <span key={tag}>{tag}</span>)}</div>
        <h2>Files</h2>
        {model.files.map((f) => <a className="file" key={f.id} href={`/files/${model.id}/${f.id}`}><Download size={16} />{f.filename}<span>{formatBytes(f.sizeBytes)}</span></a>)}
      </aside>
    </section>
  );
}

function UploadPage() {
  const [file, setFile] = useState<File | null>(null);
  const [done, setDone] = useState('');
  async function submit(e: FormEvent) {
    e.preventDefault();
    if (!file) return;
    const fd = new FormData();
    fd.append('file', file);
    const model = await api('/api/models', { method: 'POST', body: fd }) as Model;
    setDone(model.id);
  }
  return <section className="content narrow"><form className="upload" onSubmit={submit}><input type="file" onChange={(e) => setFile(e.target.files?.[0] ?? null)} /><button><Upload size={18} />Upload</button>{done && <a href={`/models/${done}`}>Open model</a>}</form></section>;
}

function SettingsPage() {
  const { data } = useQuery<{ totalBytes: number }>({ queryKey: ['storage'], queryFn: () => api('/api/storage') });
  const { data: shares } = useQuery<Share[]>({ queryKey: ['shares'], queryFn: () => api('/api/shares') });
  return <section className="content narrow"><h1>Settings</h1><p>Storage: {formatBytes(data?.totalBytes ?? 0)}</p><h2>Share links</h2>{shares?.map((s) => <div className="file" key={s.id}><span>{s.label || s.scope}</span><a href={`/s/${s.token}`}>Open</a></div>)}</section>;
}

type Collection = { id: string; name: string; slug: string; description: string; models?: Model[] };
type Share = { id: string; token: string; scope: string; label?: string };

function CollectionsPage() {
  const { data } = useQuery<Collection[]>({ queryKey: ['collections'], queryFn: () => api('/api/collections') });
  return <section className="content"><div className="grid">{(data ?? []).map((c) => <a className="card" href={`/collections/${c.slug}`} key={c.id}><div className="thumb"><Box size={42} /></div><h2>{c.name}</h2><p>{c.description}</p></a>)}</div></section>;
}

function CollectionDetail({ slug }: { slug: string }) {
  const { data } = useQuery<Collection>({ queryKey: ['collection', slug], queryFn: () => api(`/api/collections/${slug}`) });
  return <section className="content"><h1>{data?.name}</h1><p>{data?.description}</p><div className="grid">{data?.models?.map((model) => <ModelCard model={model} key={model.id} />)}</div></section>;
}

function PublicPage({ token }: { token: string }) {
  const { data } = useQuery<{ model?: Model; collection?: Collection }>({ queryKey: ['public', token], queryFn: () => api(`/api/public/${token}`) });
  if (data?.collection) {
    return <main className="app"><section className="content"><h1>{data.collection.name}</h1><div className="grid">{data.collection.models?.map((model) => <PublicCard model={model} token={token} key={model.id} />)}</div></section></main>;
  }
  if (data?.model) {
    return <main className="app"><section className="detail"><div className="viewer"><PublicCard model={data.model} token={token} /></div><aside className="panel"><h1>{data.model.title}</h1><p>{data.model.description}</p>{data.model.files.map((f) => <a className="file" key={f.id} href={`/api/public/${token}/files/${f.id}`}><Download size={16} />{f.filename}</a>)}</aside></section></main>;
  }
  return <main className="app"><div className="empty">Loading</div></main>;
}

function PublicCard({ model, token }: { model: Model; token: string }) {
  return <a className="card" href={`/s/${token}`}><div className="thumb">{model.primaryThumb ? <img src={`/api/public/${token}/thumbs/${model.primaryThumb}?model=${model.id}`} alt="" /> : <Box size={42} />}</div><h2>{model.title}</h2><p>{formatBytes(model.totalBytes)}</p></a>;
}

async function api(path: string, init: RequestInit = {}) {
  const headers = init.body instanceof FormData ? init.headers : { 'Content-Type': 'application/json', ...init.headers };
  const res = await fetch(path, { credentials: 'include', ...init, headers });
  if (!res.ok) throw new Error(await res.text());
  if (res.status === 204) return null;
  return res.json();
}

function formatBytes(n: number) {
  if (!n) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB'];
  const i = Math.min(Math.floor(Math.log(n) / Math.log(1024)), units.length - 1);
  return `${(n / 1024 ** i).toFixed(i ? 1 : 0)} ${units[i]}`;
}
