// CI専用。Aの所有cleanup確認後だけBを実行し、生の子process出力を保存しない。
import { createHash } from 'node:crypto';
import { lstat, readFile, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { parseArgs } from 'node:util';
import { runSequential, runPositiveControl } from './baseline-sequential.ts';
import { executeOwnedProcess } from './baseline-process.ts';
import { copyBaselineTools } from './baseline-tools-copy.ts';

async function bounded(name: string, limit: number): Promise<Buffer> {
  const stat = await lstat(name);
  if (!stat.isFile() || stat.isSymbolicLink() || stat.size > limit) throw new Error();
  const data = await readFile(name); if (data.length > limit) throw new Error(); return data;
}
async function digest(name: string) { return createHash('sha256').update(await bounded(name, 64 * 1024 * 1024)).digest('hex'); }
async function invoke(supervisor: string, tools: string, output: string, exe: string, manifest: string, mode: string, runId: string) {
  return await executeOwnedProcess('pwsh.exe', ['-NoProfile', '-NonInteractive', '-File', supervisor, '-ToolsDirectory', tools,
    '-OutputDirectory', output, '-ApplicationPath', exe, '-BuildManifest', manifest, '-Mode', mode, '-RunId', runId], 180000);
}
let summaryPath: string | undefined;
try {
  const { values: v } = parseArgs({ options: { toolsA: { type: 'string' }, toolsB: { type: 'string' }, outputA: { type: 'string' },
    outputB: { type: 'string' }, exe: { type: 'string' }, manifest: { type: 'string' }, summary: { type: 'string' }, supervisor: { type: 'string' }, config: { type: 'string' }, positive: { type: 'boolean', default: false } } });
  if (process.platform !== 'win32' || !v.toolsA || !v.toolsB || !v.outputA || !v.outputB || !v.exe || !v.manifest || !v.summary || !v.supervisor || !v.config) throw new Error();
  const runId = process.env.BASELINE_RUN_ID, parent = process.env.RUNNER_TEMP;
  if (!runId || !/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/.test(runId) || !parent) throw new Error();
  const root = path.join(path.resolve(parent), `rescue-wry-${runId}`);
  const rootStat = await lstat(root);
  if (!rootStat.isDirectory() || rootStat.isSymbolicLink()) throw new Error();
  const owner: unknown = JSON.parse((await bounded(path.join(root, 'owner.json'), 65536)).toString('utf8'));
  if (!owner || typeof owner !== 'object' || Array.isArray(owner)) throw new Error();
  const marker = owner as Record<string, unknown>;
  if (marker.schema !== 1 || marker.purpose !== 'wry-sequential-owned' || marker.runId !== runId) throw new Error();
  const same = (a: string, b: string) => path.resolve(a).toLowerCase() === path.join(root, b).toLowerCase();
  if (!same(v.toolsA, 'baseline-tools') || !same(v.toolsB, 'baseline-tools-b') || !same(v.outputA, 'baseline-a-evidence')
    || !same(v.outputB, 'baseline-b-evidence') || !same(v.manifest, 'baseline-build.json') || !same(v.summary, 'baseline-sequential.json')) throw new Error();
  summaryPath = v.summary;
  const unique = [v.toolsA, v.toolsB, v.outputA, v.outputB, v.summary].map(x => path.resolve(x).toLowerCase());
  if (new Set(unique).size !== unique.length) throw new Error();
  const manifest: unknown = JSON.parse((await bounded(v.manifest, 65536)).toString('utf8'));
  const config: unknown = JSON.parse((await bounded(v.config, 65536)).toString('utf8'));
  if (!manifest || typeof manifest !== 'object' || Array.isArray(manifest) || !config || typeof config !== 'object' || Array.isArray(config)) throw new Error();
  const m = manifest as Record<string, unknown>, c = config as Record<string, unknown>;
  if (m.schema !== 1 || m.kind !== 'direct-wry-controller-contrast' || m.featureTreeMatched !== true || m.distribution !== false
    || typeof m.sourceCommit !== 'string' || !/^[0-9a-f]{40}$/.test(m.sourceCommit)
    || typeof m.exeSha256 !== 'string' || await digest(v.exe) !== m.exeSha256
    || typeof c.tauriDriverVersion !== 'string' || !/^\d+\.\d+\.\d+$/.test(c.tauriDriverVersion)) throw new Error();
  const expected = { sourceCommit: m.sourceCommit, exeSha256: m.exeSha256, runId };
  const firstMode = v.positive ? 'blank-script' : 'with-html';
  const first = async () => {
    const outcome = await invoke(v.supervisor!, v.toolsA!, v.outputA!, v.exe!, v.manifest!, firstMode, runId);
    let report: unknown = null;
    try { report = JSON.parse((await bounded(path.join(v.outputA!, 'native-result.json'), 65536)).toString('utf8')) as unknown; } catch { /* 欠測は後続停止。 */ }
    return { outcome, report };
  };
  const second = async () => {
    await copyBaselineTools(v.toolsA!, v.toolsB!, c.tauriDriverVersion as string);
    const outcome = await invoke(v.supervisor!, v.toolsB!, v.outputB!, v.exe!, v.manifest!, 'load-html', runId);
    let report: unknown = null;
    try { report = JSON.parse((await bounded(path.join(v.outputB!, 'native-result.json'), 65536)).toString('utf8')) as unknown; } catch { /* 欠測は未確認。 */ }
    return { outcome, report };
  };
  const result = v.positive ? await runPositiveControl(first, expected) : await runSequential(first, second, expected);
  await writeFile(v.summary, JSON.stringify({ schema: 1, purpose: 'diagnostic-only', ...expected, firstMode, ...result }), { flag: 'wx' });
  console.log('BASELINE_SEQUENTIAL_RECORDED');
  process.exitCode = result.first === 'passed' && result.cleanup === 'confirmed'
    && (v.positive ? result.second === 'not-started' : result.second === 'passed' && result.secondCleanup === 'confirmed') ? 0 : 1;
} catch {
  if (summaryPath) {
    try { await writeFile(summaryPath, JSON.stringify({ schema: 1, purpose: 'diagnostic-only', outcome: 'incomplete' }), { flag: 'wx' }); }
    catch { /* 記録できない場合も元の失敗を保持する。 */ }
  }
  console.log('BASELINE_SEQUENTIAL_INCOMPLETE'); process.exitCode = 1;
}
