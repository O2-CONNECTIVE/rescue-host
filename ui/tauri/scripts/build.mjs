import { mkdir, copyFile, rm } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import path from 'node:path';

const root = fileURLToPath(new URL('../', import.meta.url));
const output = path.join(root, 'dist');
await rm(output, { recursive: true, force: true });
await mkdir(output);
for (const filename of ['index.html', 'app.mjs', 'controller.mjs', 'app.css']) {
  await copyFile(path.join(root, 'web', filename), path.join(output, filename));
}
for (const filename of ['tokens.css', 'components.css']) {
  await copyFile(path.join(root, 'design', filename), path.join(output, filename));
}
console.log('Built six local shell assets; no remote assets or backend binaries.');
