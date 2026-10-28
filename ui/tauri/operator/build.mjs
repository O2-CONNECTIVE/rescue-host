import { build } from 'vite';
import { copyFile } from 'node:fs/promises';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { fileURLToPath } from 'node:url';
const root = fileURLToPath(new URL('../', import.meta.url));
// Reuse the canonical notice generator/checker without changing its CLI.
await promisify(execFile)(process.execPath, ['scripts/notices.mjs', '--check'], { cwd: root });
await build({ configFile: fileURLToPath(new URL('./vite.config.ts', import.meta.url)) });
await copyFile(new URL('../dist/THIRD_PARTY_NOTICES.txt', import.meta.url), new URL('./dist/THIRD_PARTY_NOTICES.txt', import.meta.url));
