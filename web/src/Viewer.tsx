import { Bounds, Edges, OrbitControls, useBounds, type BoundsApi } from '@react-three/drei';
import { Canvas, useLoader } from '@react-three/fiber';
import { RotateCcw, ZoomIn, ZoomOut } from 'lucide-react';
import { Suspense, useEffect, useMemo, useRef, type ComponentRef } from 'react';
import { Color, Mesh, MeshStandardMaterial, type Object3D } from 'three';
import { STLLoader } from 'three/examples/jsm/loaders/STLLoader.js';
import { OBJLoader } from 'three/examples/jsm/loaders/OBJLoader.js';
import { ThreeMFLoader } from 'three/examples/jsm/loaders/3MFLoader.js';
import type { ModelFile } from './App';
import { prepareSTLGeometry } from './viewerGeometry';
import { DEFAULT_MODEL_COLOR, getModelColor } from './viewerPreferences';

export default function ModelViewer({ file, url }: { file: ModelFile; url: string }) {
  const bounds = useRef<BoundsApi | null>(null);
  const controls = useRef<ComponentRef<typeof OrbitControls>>(null);
  const modelColor = getModelColor();
  const edgeColor = useMemo(() => `#${new Color(modelColor).multiplyScalar(0.45).getHexString()}`, [modelColor]);
  const zoom = (direction: 'in' | 'out') => {
    if (!controls.current) return;
    controls.current[direction === 'in' ? 'dollyIn' : 'dollyOut'](1.2);
    controls.current.update();
  };
  return (
    <>
      <Canvas shadows dpr={[1, 2]} camera={{ position: [4, 3.2, 4], fov: 32 }} gl={{ alpha: true, antialias: true }}>
        <ambientLight intensity={0.75} />
        <hemisphereLight color="#f5fff9" groundColor="#42675c" intensity={0.9} />
        <directionalLight castShadow color="#fffaf0" intensity={1.8} position={[5, 7, 4]} />
        <directionalLight color="#c8eee3" intensity={0.55} position={[-4, 2, -3]} />
        <Suspense fallback={null}>
          <Bounds fit clip observe margin={1.25}>
            <BoundsHandle apiRef={bounds} />
            <group rotation={[-Math.PI / 2, 0, 0]}>
              <Loaded file={file} url={url} color={modelColor} edgeColor={edgeColor} />
            </group>
          </Bounds>
        </Suspense>
        <OrbitControls ref={controls} makeDefault enableDamping dampingFactor={0.08} minPolarAngle={0.12} maxPolarAngle={Math.PI / 2.05} />
      </Canvas>
      <div className="viewer-controls" role="group" aria-label="3D viewer controls">
        <button type="button" className="viewer-control" aria-label="Zoom in" title="Zoom in" onClick={() => zoom('in')}><ZoomIn size={18} /></button>
        <button type="button" className="viewer-control" aria-label="Zoom out" title="Zoom out" onClick={() => zoom('out')}><ZoomOut size={18} /></button>
        <button type="button" className="viewer-control" aria-label="Reset view" title="Reset view" onClick={() => bounds.current?.refresh().clip().fit()}><RotateCcw size={18} /></button>
      </div>
      <div className="viewer-hint" aria-hidden>Drag to rotate · Scroll to zoom</div>
    </>
  );
}

function BoundsHandle({ apiRef }: { apiRef: { current: BoundsApi | null } }) {
  const api = useBounds();
  useEffect(() => {
    apiRef.current = api;
    return () => { apiRef.current = null; };
  }, [api, apiRef]);
  return null;
}

function Loaded({ file, url, color, edgeColor }: { file: ModelFile; url: string; color: string; edgeColor: string }) {
  if (file.format === 'obj') {
    return <OBJView url={url} color={color} />;
  }
  if (file.format === '3mf') {
    return <ThreeMFView url={url} />;
  }
  return <STLView url={url} color={color} edgeColor={edgeColor} />;
}

function OBJView({ url, color }: { url: string; color: string }) {
  const obj = useLoader(OBJLoader, url);
  return <PreparedObject object={obj} color={color} recolor />;
}

function ThreeMFView({ url }: { url: string }) {
  const obj = useLoader(ThreeMFLoader, url);
  return <PreparedObject object={obj} />;
}

function STLView({ url, color, edgeColor }: { url: string; color: string; edgeColor: string }) {
  const source = useLoader(STLLoader, url);
  const geometry = useMemo(() => prepareSTLGeometry(source), [source]);
  useEffect(() => () => geometry.dispose(), [geometry]);
  return <mesh geometry={geometry} castShadow receiveShadow><meshStandardMaterial color={color} roughness={0.48} metalness={0.03} /><Edges color={edgeColor} opacity={0.24} threshold={28} transparent /></mesh>;
}

function PreparedObject({ object, color = DEFAULT_MODEL_COLOR, recolor = false }: { object: Object3D; color?: string; recolor?: boolean }) {
  const material = useMemo(() => new MeshStandardMaterial({ color, roughness: 0.48, metalness: 0.03 }), [color]);
  const prepared = useMemo(() => {
    // Three.js clones share useLoader-cached geometry and source materials. Dispose only
    // the replacement material allocated here; disposing shared resources breaks the cache.
    const clone = object.clone(true);
    clone.traverse((child) => {
      if (!(child instanceof Mesh)) return;
      child.castShadow = true;
      child.receiveShadow = true;
      if (recolor) child.material = material;
    });
    return clone;
  }, [material, object, recolor]);
  useEffect(() => () => material.dispose(), [material]);
  return <primitive object={prepared} />;
}
