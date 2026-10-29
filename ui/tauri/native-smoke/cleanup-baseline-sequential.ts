import { lstat, readFile, writeFile, readdir, unlink, rmdir } from 'node:fs/promises';
import path from 'node:path';
import { cleanupBaselineArms, cleanupBaselineShared } from './baseline-cleanup.ts';

async function read(name: string): Promise<unknown> {
  const stat = await lstat(name); if (!stat.isFile() || stat.isSymbolicLink() || stat.size > 65536) throw new Error();
  const data = await readFile(name); if (data.length > 65536) throw new Error(); return JSON.parse(data.toString('utf8')) as unknown;
}
async function absent(name: string): Promise<boolean> {
  try { await lstat(name); return false; } catch (error) { return (error as NodeJS.ErrnoException).code === 'ENOENT'; }
}
let reportPath: string | undefined;
try {
  if (process.argv.length !== 3 || !path.isAbsolute(process.argv[2])) throw new Error();
  const root = path.resolve(process.argv[2]), parent = process.env.RUNNER_TEMP;
  if (!parent || !path.isAbsolute(parent) || path.dirname(root) !== path.resolve(parent)) throw new Error();
  const rootStat = await lstat(root);
  if (!rootStat.isDirectory() || rootStat.isSymbolicLink()) throw new Error();
  const owner = await read(path.join(root, 'owner.json'));
  if (!owner || typeof owner !== 'object' || Array.isArray(owner)) throw new Error();
  const o = owner as Record<string, unknown>;
  if (o.schema !== 1 || o.purpose !== 'wry-sequential-owned' || typeof o.runId !== 'string'
    || !/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/.test(o.runId)
    || path.basename(root) !== `rescue-wry-${o.runId}`) throw new Error();
  reportPath = path.join(path.resolve(parent), `baseline-cleanup-${o.runId}.json`);
  const manifest = await read(path.join(root, 'baseline-build.json')), summary = await read(path.join(root, 'baseline-sequential.json'));
  if (!manifest || typeof manifest !== 'object' || Array.isArray(manifest) || !summary || typeof summary !== 'object' || Array.isArray(summary)) throw new Error();
  const m = manifest as Record<string, unknown>, s = summary as Record<string, unknown>;
  if (m.schema !== 1 || m.kind !== 'direct-wry-controller-contrast' || m.featureTreeMatched !== true || m.distribution !== false
    || s.schema !== 1 || s.purpose !== 'diagnostic-only' || s.runId !== o.runId || s.sourceCommit !== m.sourceCommit || s.exeSha256 !== m.exeSha256) throw new Error();
  const expected = { sourceCommit: m.sourceCommit, exeSha256: m.exeSha256, runId: s.runId };
  let first: unknown = null, second: unknown = null;
  if (s.cleanup === 'confirmed') first = await read(path.join(root, 'baseline-a-evidence/native-result.json'));
  if (s.secondCleanup === 'confirmed') second = await read(path.join(root, 'baseline-b-evidence/native-result.json'));
  const result = await cleanupBaselineArms(root, expected, first, second);
  if (s.second === 'not-started' && await absent(path.join(root, 'baseline-tools-b')) && await absent(path.join(root, 'baseline-b-evidence'))) result.second = 'not-started';
  const shared = await cleanupBaselineShared(root, result);
  let rootResult = 'unconfirmed';
  if (shared === 'removed' && (await readdir(root)).length === 1 && (await readdir(root))[0] === 'owner.json') {
    await unlink(path.join(root, 'owner.json')); await rmdir(root);
    if (await absent(root)) rootResult = 'removed';
  }
  await writeFile(reportPath, JSON.stringify({ schema: 1, ...result, shared, root: rootResult }), { flag: 'wx' });
  console.log('BASELINE_CLEANUP_RECORDED');
  process.exitCode = result.first === 'removed' && ['removed', 'not-started'].includes(result.second) && shared === 'removed' && rootResult === 'removed' ? 0 : 1;
} catch {
  if (reportPath) { try { await writeFile(reportPath, JSON.stringify({ schema: 1, first: 'unconfirmed', second: 'unconfirmed', root: 'unconfirmed' }), { flag: 'wx' }); } catch { /* 記録不能も失敗。 */ } }
  console.log('BASELINE_CLEANUP_UNCONFIRMED'); process.exitCode = 1;
}
