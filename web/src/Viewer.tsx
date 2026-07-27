import { Bounds, Edges, OrbitControls, Stage } from '@react-three/drei';
import { Canvas, useLoader } from '@react-three/fiber';
import { Suspense, useEffect, useMemo } from 'react';
import { Mesh, MeshStandardMaterial, type Object3D } from 'three';
import { STLLoader } from 'three/examples/jsm/loaders/STLLoader.js';
import { OBJLoader } from 'three/examples/jsm/loaders/OBJLoader.js';
import { ThreeMFLoader } from 'three/examples/jsm/loaders/3MFLoader.js';
import type { ModelFile } from './App';
import { prepareSTLGeometry } from './viewerGeometry';

const MODEL_COLOR = '#4f9f88';
const EDGE_COLOR = '#174b3e';

export default function ModelViewer({ file, url }: { file: ModelFile; url: string }) {
  return (
    <>
      <Canvas shadows dpr={[1, 2]} camera={{ position: [4, 3.2, 4], fov: 32 }} gl={{ alpha: true, antialias: true }}>
        <ambientLight intensity={0.75} />
        <hemisphereLight color="#f5fff9" groundColor="#42675c" intensity={0.9} />
        <directionalLight castShadow color="#fffaf0" intensity={1.8} position={[5, 7, 4]} />
        <directionalLight color="#c8eee3" intensity={0.55} position={[-4, 2, -3]} />
        <Suspense fallback={null}>
          <Bounds fit clip observe margin={1.25}>
            <Stage adjustCamera={false} environment="city" intensity={0.75} preset="rembrandt" shadows={{ type: 'contact', opacity: 0.3, blur: 2.5 }}>
              <group rotation={[-Math.PI / 2, 0, 0]}>
                <Loaded file={file} url={url} />
              </group>
            </Stage>
          </Bounds>
        </Suspense>
        <OrbitControls makeDefault enableDamping dampingFactor={0.08} minPolarAngle={0.12} maxPolarAngle={Math.PI / 2.05} />
      </Canvas>
      <div className="viewer-hint" aria-hidden>Drag to rotate · Scroll to zoom</div>
    </>
  );
}

function Loaded({ file, url }: { file: ModelFile; url: string }) {
  if (file.format === 'obj') {
    return <OBJView url={url} />;
  }
  if (file.format === '3mf') {
    return <ThreeMFView url={url} />;
  }
  return <STLView url={url} />;
}

function OBJView({ url }: { url: string }) {
  const obj = useLoader(OBJLoader, url);
  return <PreparedObject object={obj} recolor />;
}

function ThreeMFView({ url }: { url: string }) {
  const obj = useLoader(ThreeMFLoader, url);
  return <PreparedObject object={obj} />;
}

function STLView({ url }: { url: string }) {
  const source = useLoader(STLLoader, url);
  const geometry = useMemo(() => prepareSTLGeometry(source), [source]);
  useEffect(() => () => geometry.dispose(), [geometry]);
  return <mesh geometry={geometry} castShadow receiveShadow><meshStandardMaterial color={MODEL_COLOR} roughness={0.48} metalness={0.03} /><Edges color={EDGE_COLOR} opacity={0.24} threshold={28} transparent /></mesh>;
}

function PreparedObject({ object, recolor = false }: { object: Object3D; recolor?: boolean }) {
  const material = useMemo(() => new MeshStandardMaterial({ color: MODEL_COLOR, roughness: 0.48, metalness: 0.03 }), []);
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
