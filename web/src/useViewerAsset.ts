import { useEffect, useState } from 'react';
import type { ModelFile } from './App';
import { loadViewerAsset, type ViewerAsset } from './viewerResources';

export function useViewerAsset(url: string, format: ModelFile['format'], color: string) {
  const key = JSON.stringify([url, format, color]);
  const [state, setState] = useState<{ key: string; asset?: ViewerAsset; error?: Error }>({ key });
  useEffect(() => {
    const controller = new AbortController();
    let loaded: ViewerAsset | undefined;
    setState({ key });
    void loadViewerAsset(url, format, color, controller.signal).then((asset) => {
      loaded = asset;
      if (controller.signal.aborted) {
        asset.dispose();
      } else {
        setState({ key, asset });
      }
    }).catch((error: unknown) => {
      if (!controller.signal.aborted) setState({ key, error: error instanceof Error ? error : new Error('The model could not be loaded') });
    });
    return () => {
      controller.abort();
      loaded?.dispose();
    };
  }, [key, url, format, color]);
  return state.key === key ? state : { key };
}
