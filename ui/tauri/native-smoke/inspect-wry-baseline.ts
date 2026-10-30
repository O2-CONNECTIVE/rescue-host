import { readFile, lstat } from 'node:fs/promises';
import path from 'node:path';
import { createHash } from 'node:crypto';
import { parseArgs } from 'node:util';
import { decideBaseline } from './baseline-decision.ts';
async function bounded(name: string, limit: number) {
  const stat = await lstat(name);
  if (!stat.isFile() || stat.isSymbolicLink() || stat.size > limit) throw new Error();
  const bytes = await readFile(name); if (bytes.length > limit) throw new Error(); return bytes;
}
function object(x: unknown): x is Record<string, unknown> { return !!x && typeof x === 'object' && !Array.isArray(x); }
try {
  const { values: v } = parseArgs({ options: { directory: { type: 'string' }, source: { type: 'string' },
    exe: { type: 'string' }, build: { type: 'string' }, mode: { type: 'string' }, runId: { type: 'string' } } });
  if (!v.directory || !v.exe || !v.build || !v.source || !/^[0-9a-f]{40}$/.test(v.source)
    || !v.mode || !['with-html', 'load-html', 'blank-script'].includes(v.mode)
    || !v.runId || !/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/.test(v.runId)) throw new Error();
  const exeSha256 = createHash('sha256').update(await bounded(v.exe, 64 * 1024 * 1024)).digest('hex');
  const build: unknown = JSON.parse((await bounded(v.build, 8192)).toString());
  const collection: unknown = JSON.parse((await bounded(path.join(v.directory, 'native-result.json'), 65536)).toString());
  const wry: unknown = JSON.parse((await bounded(path.join(v.directory, 'wry-startup-report.json'), 16384)).toString());
  if (!object(build) || build.schema !== 1 || build.kind !== 'direct-wry-controller-contrast' || build.distribution !== false
    || build.sourceCommit !== v.source || build.exeSha256 !== exeSha256 || build.featureTreeMatched !== true
    || !object(collection) || collection.sourceCommit !== v.source || collection.exeSha256 !== exeSha256 || collection.mode !== v.mode || collection.runId !== v.runId
    || !object(wry) || !object(wry.provenance) || wry.provenance.sourceCommit !== v.source || wry.provenance.exeSha256 !== exeSha256) throw new Error();
  let session: unknown = null;
  try { session = JSON.parse((await bounded(path.join(v.directory, 'baseline-session.json'), 2048)).toString()); }
  catch (error) { if (!error || typeof error !== 'object' || !('code' in error) || error.code !== 'ENOENT') throw error; }
  const result = decideBaseline(collection, wry, session);
  console.log(JSON.stringify({ ...result, sourceCommit: v.source, exeSha256 }));
  process.exitCode = result.contrastPassed || ('positiveControlPassed' in result && result.positiveControlPassed === true) ? 0 : 1;
} catch {
  console.log(JSON.stringify({ schema: 1, purpose: 'diagnostic-only', productNativePass: false,
    rootCauseProven: false, contrastPassed: false, diagnostic: 'incomplete', outcome: 'rejected', nextAction: 'repair-observation' }));
  process.exitCode = 1;
}
