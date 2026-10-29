import { lstat, readFile, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { cleanupBaselineArms } from './baseline-cleanup.ts';

async function read(name: string): Promise<unknown> {
  const stat = await lstat(name); if (!stat.isFile() || stat.isSymbolicLink() || stat.size > 65536) throw new Error();
  const data = await readFile(name); if (data.length > 65536) throw new Error(); return JSON.parse(data.toString('utf8')) as unknown;
}
async function absent(name: string): Promise<boolean> {
  try { await lstat(name); return false; } catch (error) { return (error as NodeJS.ErrnoException).code === 'ENOENT'; }
}
let root: string | undefined;
try {
  if (process.argv.length !== 3 || !path.isAbsolute(process.argv[2])) throw new Error();
  root = process.argv[2];
  const manifest = await read(path.join(root, 'baseline-build.json')), summary = await read(path.join(root, 'baseline-sequential.json'));
  if (!manifest || typeof manifest !== 'object' || Array.isArray(manifest) || !summary || typeof summary !== 'object' || Array.isArray(summary)) throw new Error();
  const m = manifest as Record<string, unknown>, s = summary as Record<string, unknown>;
  if (m.schema !== 1 || m.kind !== 'direct-wry-controller-contrast' || m.featureTreeMatched !== true || m.distribution !== false
    || s.schema !== 1 || s.purpose !== 'diagnostic-only' || s.sourceCommit !== m.sourceCommit || s.exeSha256 !== m.exeSha256) throw new Error();
  const expected = { sourceCommit: m.sourceCommit, exeSha256: m.exeSha256, runId: s.runId };
  let first: unknown = null, second: unknown = null;
  if (s.cleanup === 'confirmed') first = await read(path.join(root, 'baseline-a-evidence/native-result.json'));
  if (s.secondCleanup === 'confirmed') second = await read(path.join(root, 'baseline-b-evidence/native-result.json'));
  const result = await cleanupBaselineArms(root, expected, first, second);
  if (s.second === 'not-started' && await absent(path.join(root, 'baseline-tools-b')) && await absent(path.join(root, 'baseline-b-evidence'))) result.second = 'not-started';
  await writeFile(path.join(root, 'baseline-cleanup.json'), JSON.stringify({ schema: 1, ...result }), { flag: 'wx' });
  console.log('BASELINE_CLEANUP_RECORDED');
  process.exitCode = result.first === 'removed' && ['removed', 'not-started'].includes(result.second) ? 0 : 1;
} catch {
  if (root) { try { await writeFile(path.join(root, 'baseline-cleanup.json'), JSON.stringify({ schema: 1, first: 'unconfirmed', second: 'unconfirmed' }), { flag: 'wx' }); } catch { /* 記録不能も失敗。 */ } }
  console.log('BASELINE_CLEANUP_UNCONFIRMED'); process.exitCode = 1;
}
