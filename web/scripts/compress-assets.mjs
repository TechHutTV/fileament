import { readdir, readFile, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { argv } from 'node:process';
import { fileURLToPath, pathToFileURL, URL } from 'node:url';
import { gzipSync } from 'node:zlib';

export async function compressAssets(directory) {
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    if (!entry.isFile() || !/^.+-[A-Za-z0-9_-]{8}\.(js|css)$/.test(entry.name)) continue;
    const path = join(directory, entry.name);
    await writeFile(path + '.gz', gzipSync(await readFile(path), { level: 9 }));
  }
}

if (argv[1] && import.meta.url === pathToFileURL(argv[1]).href) {
  await compressAssets(fileURLToPath(new URL('../dist/assets/', import.meta.url)));
}
