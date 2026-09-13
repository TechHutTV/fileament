import { Bounds, Edges, OrbitControls, useBounds, type BoundsApi } from '@react-three/drei';
import { Canvas } from '@react-three/fiber';
import { RotateCcw, ZoomIn, ZoomOut } from 'lucide-react';
import { useEffect, useMemo, useRef, useState, type ComponentRef } from 'react';
import { Color } from 'three';
import type { ModelFile } from './App';
import { getModelColor } from './viewerPreferences';
import { useViewerAsset } from './useViewerAsset';
import { VIEWER_DETAIL_TRIANGLES } from './viewerResources';

type ReadyModel = { key: string; triangles: number };

export default function ModelViewer({ file, url }: { file: ModelFile; url: string }) {
  const bounds = useRef<BoundsApi | null>(null);
  const controls = useRef<ComponentRef<typeof OrbitControls>>(null);
  const modelColor = getModelColor();
  const [ready, setReady] = useState<ReadyModel | null>(null);
  const loaded = ready?.key === JSON.stringify([url, file.format, modelColor]);
  const detailed = loaded && ready.triangles > 0 && ready.triangles <= VIEWER_DETAIL_TRIANGLES;
  const edgeColor = useMemo(() => `#${new Color(modelColor).multiplyScalar(0.45).getHexString()}`, [modelColor]);
  const zoom = (direction: 'in' | 'out') => {
    if (!controls.current) return;
    // three-stdlib's dollyOut divides the radius scale, moving perspective cameras closer.
    controls.current[direction === 'in' ? 'dollyOut' : 'dollyIn'](1.2);
    controls.current.update();
  };
  return (
    <>
      <Canvas frameloop="demand" shadows={detailed} dpr={[1, 2]} camera={{ position: [4, 3.2, 4], fov: 32 }} gl={{ alpha: true, antialias: true }}>
        <ambientLight intensity={0.75} />
        <hemisphereLight color="#f5fff9" groundColor="#42675c" intensity={0.9} />
        <directionalLight castShadow={detailed} color="#fffaf0" intensity={1.8} position={[5, 7, 4]} />
        <directionalLight color="#c8eee3" intensity={0.55} position={[-4, 2, -3]} />
        <ViewerScene url={url} format={file.format} color={modelColor} edgeColor={edgeColor} bounds={bounds} onReady={setReady} />
        <OrbitControls ref={controls} makeDefault enableDamping dampingFactor={0.08} minPolarAngle={0.12} maxPolarAngle={Math.PI / 2.05} />
      </Canvas>
      <div className="viewer-controls" role="group" aria-label="3D viewer controls">
        <button type="button" className="viewer-control" disabled={!loaded} aria-label="Zoom in" title="Zoom in" onClick={() => zoom('in')}><ZoomIn size={18} /></button>
        <button type="button" className="viewer-control" disabled={!loaded} aria-label="Zoom out" title="Zoom out" onClick={() => zoom('out')}><ZoomOut size={18} /></button>
        <button type="button" className="viewer-control" disabled={!loaded} aria-label="Reset view" title="Reset view" onClick={() => bounds.current?.refresh().clip().fit()}><RotateCcw size={18} /></button>
      </div>
      {loaded ? <div className="viewer-hint" aria-hidden>Drag to rotate · Scroll to zoom</div> : <div className="viewer-hint" role="status">Loading 3D view…</div>}
    </>
  );
}

export function ViewerScene({ url, format, color, edgeColor, bounds, onReady }: { url: string; format: ModelFile['format']; color: string; edgeColor: string; bounds: { current: BoundsApi | null }; onReady: (model: ReadyModel | null) => void }) {
  // Dispose assets in the scene renderer, after their primitives detach.
  const { key, asset, error } = useViewerAsset(url, format, color);
  useEffect(() => onReady(asset ? { key, triangles: asset.triangleCount } : null), [key, asset, onReady]);
  if (error) throw error;
  if (!asset) return null;
  const detailed = asset.triangleCount > 0 && asset.triangleCount <= VIEWER_DETAIL_TRIANGLES;
  return <Bounds key={url} fit clip observe margin={1.25}>
    <BoundsHandle apiRef={bounds} />
    <group rotation={[-Math.PI / 2, 0, 0]}>
      <primitive object={asset.object} dispose={null}>{asset.stlGeometry && detailed && <Edges geometry={asset.stlGeometry} color={edgeColor} opacity={0.24} threshold={28} transparent />}</primitive>
    </group>
  </Bounds>;
}

function BoundsHandle({ apiRef }: { apiRef: { current: BoundsApi | null } }) {
  const api = useBounds();
  useEffect(() => {
    apiRef.current = api;
    return () => { apiRef.current = null; };
  }, [api, apiRef]);
  return null;
}
