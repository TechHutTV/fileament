import { mkdtemp, readdir, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { gunzipSync } from 'node:zlib';
import { expect, test } from 'vitest';
import { compressAssets } from './compress-assets.mjs';

test('builds reproducible gzip copies of hashed scripts and styles without changing originals', async () => {
  const directory = await mkdtemp(join(tmpdir(), 'fileament-web-assets-'));
  try {
    const inputs = { 'index-1234abcd.js': 'export const name = "Fileament";'.repeat(100), 'index-aBcD_-12.css': 'body{color:green}'.repeat(100), 'plain.js': 'plain', 'photo-1234abcd.png': 'png', 'index.html': '<html></html>' };
    for (const [name, data] of Object.entries(inputs)) await writeFile(join(directory, name), data);
    await compressAssets(directory);
    const files = await readdir(directory);
    expect(files.filter((name) => name.endsWith('.gz')).sort()).toEqual(['index-1234abcd.js.gz', 'index-aBcD_-12.css.gz']);
    const first = await readFile(join(directory, 'index-1234abcd.js.gz'));
    expect(first.byteLength).toBeLessThan(inputs['index-1234abcd.js'].length);
    for (const name of ['index-1234abcd.js', 'index-aBcD_-12.css']) {
      expect(gunzipSync(await readFile(join(directory, name+'.gz'))).toString()).toBe(inputs[name]);
      expect((await readFile(join(directory, name))).toString()).toBe(inputs[name]);
    }
    await compressAssets(directory);
    expect(await readFile(join(directory, 'index-1234abcd.js.gz'))).toEqual(first);
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});
