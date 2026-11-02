import { lstat, readFile } from 'node:fs/promises';
import { mayStartSecond } from './baseline-sequential.ts';

let allowed = false;
try {
  if (process.argv.length !== 5) throw new Error();
  async function read(name: string): Promise<unknown> {
    const stat = await lstat(name);
    if (!stat.isFile() || stat.isSymbolicLink() || stat.size > 65536) throw new Error();
    const data = await readFile(name); if (data.length > 65536) throw new Error();
    return JSON.parse(data.toString('utf8')) as unknown;
  }
  const report = await read(process.argv[2]); const manifest = await read(process.argv[3]);
  if (!manifest || typeof manifest !== 'object' || Array.isArray(manifest)) throw new Error();
  const m = manifest as Record<string, unknown>;
  if (m.schema !== 1 || m.kind !== 'direct-wry-controller-contrast' || m.featureTreeMatched !== true || m.distribution !== false) throw new Error();
  allowed = mayStartSecond(report, { sourceCommit: m.sourceCommit, exeSha256: m.exeSha256, runId: process.argv[4] });
} catch { /* 生のpath、例外、入力を出力しない。 */ }
console.log(allowed ? 'BASELINE_CLEANUP_CONFIRMED' : 'BASELINE_CLEANUP_UNCONFIRMED');
process.exitCode = allowed ? 0 : 1;
