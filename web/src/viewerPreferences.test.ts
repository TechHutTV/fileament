import { afterEach, expect, test, vi } from 'vitest';
import { DEFAULT_MODEL_COLOR, getModelColor, saveModelColor } from './viewerPreferences';

afterEach(() => {
  vi.restoreAllMocks();
  localStorage.clear();
});

test('falls back to the default for a malformed stored color', () => {
  localStorage.setItem('fileament-model-color', 'not-a-color');

  expect(getModelColor()).toBe(DEFAULT_MODEL_COLOR);
});

test('falls back to the default when browser storage cannot be read', () => {
  vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
    throw new DOMException('Storage is disabled', 'SecurityError');
  });

  expect(getModelColor()).toBe(DEFAULT_MODEL_COLOR);
});

test('keeps the selected color when browser storage cannot be written', () => {
  vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
    throw new DOMException('Storage quota exceeded', 'QuotaExceededError');
  });

  expect(saveModelColor('#C47742')).toBe('#c47742');
});
