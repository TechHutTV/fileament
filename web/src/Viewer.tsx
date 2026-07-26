import { Bounds, OrbitControls, Stage } from '@react-three/drei';
import { Canvas, useLoader } from '@react-three/fiber';
import { Suspense } from 'react';
import { STLLoader } from 'three/examples/jsm/loaders/STLLoader.js';
import { OBJLoader } from 'three/examples/jsm/loaders/OBJLoader.js';
import { ThreeMFLoader } from 'three/examples/jsm/loaders/3MFLoader.js';
import type { ModelFile } from './App';

export default function ModelViewer({ file, url }: { file: ModelFile; url: string }) {
  return (
    <Canvas camera={{ position: [4, 4, 4], fov: 35 }}>
      <color attach="background" args={['#f7f7f4']} />
      <Suspense fallback={null}>
        <Bounds fit clip observe margin={1.2}>
          <Stage adjustCamera={false} intensity={0.8}>
            <Loaded file={file} url={url} />
          </Stage>
        </Bounds>
      </Suspense>
      <OrbitControls makeDefault />
    </Canvas>
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
  return <primitive object={obj} />;
}

function ThreeMFView({ url }: { url: string }) {
  const obj = useLoader(ThreeMFLoader, url);
  return <primitive object={obj} />;
}

function STLView({ url }: { url: string }) {
  const geometry = useLoader(STLLoader, url);
  return <mesh geometry={geometry}><meshStandardMaterial color="#9ca3af" roughness={0.7} /></mesh>;
}
