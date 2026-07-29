export const DEFAULT_MODEL_COLOR = '#4f9f88';
export const MODEL_COLOR_STORAGE_KEY = 'fileament-model-color';

const HEX_COLOR = /^#[0-9a-f]{6}$/i;

export function getModelColor() {
  try {
    const stored = localStorage.getItem(MODEL_COLOR_STORAGE_KEY);
    return stored && HEX_COLOR.test(stored) ? stored.toLowerCase() : DEFAULT_MODEL_COLOR;
  } catch {
    return DEFAULT_MODEL_COLOR;
  }
}

export function saveModelColor(color: string) {
  const next = HEX_COLOR.test(color) ? color.toLowerCase() : DEFAULT_MODEL_COLOR;
  try {
    localStorage.setItem(MODEL_COLOR_STORAGE_KEY, next);
  } catch {
    // The current selection remains usable even when browser storage is unavailable.
  }
  return next;
}
