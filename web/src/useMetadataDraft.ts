import { useEffect, useRef, useState } from 'react';

function sameFields<T extends Record<string, string>>(a: T, b: T) {
  return Object.keys(b).every((key) => a[key] === b[key]);
}

function mergeUntouched<T extends Record<string, string>>(values: T, previous: T, incoming: T): T {
  const merged = { ...values };
  for (const key of Object.keys(incoming) as (keyof T)[]) {
    if (values[key] === previous[key]) merged[key] = incoming[key];
  }
  return merged;
}

export function useMetadataDraft<T extends Record<string, string>>(incoming: T) {
  const [draft, setDraft] = useState({ incoming, baseline: incoming, values: incoming });
  const [status, setStatus] = useState<'idle' | 'saving' | 'saved' | 'error'>('idle');
  const pending = useRef(false);
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => { mounted.current = false; };
  }, []);
  useEffect(() => {
    setDraft((current) => sameFields(current.incoming, incoming) ? current : {
      incoming, baseline: incoming,
      values: mergeUntouched(current.values, current.baseline, incoming),
    });
  }, [incoming]);

  const change = (key: keyof T, value: string) => {
    setDraft((current) => ({ ...current, values: { ...current.values, [key]: value } }));
    if (!pending.current) setStatus('idle');
  };
  const save = async (submit: (values: T) => Promise<T>) => {
    if (pending.current) return;
    pending.current = true;
    setStatus('saving');
    const submitted = draft.values;
    try {
      const saved = await submit(submitted);
      if (!mounted.current) return;
      setDraft((current) => ({ ...current, baseline: saved, values: mergeUntouched(current.values, submitted, saved) }));
      setStatus('saved');
    } catch {
      if (mounted.current) setStatus('error');
    } finally {
      pending.current = false;
    }
  };
  return { values: draft.values, change, save, status, dirty: !sameFields(draft.values, draft.baseline) };
}
