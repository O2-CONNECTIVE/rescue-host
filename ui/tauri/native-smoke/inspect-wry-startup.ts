import { readFile, lstat } from 'node:fs/promises';
import { createHash } from 'node:crypto';
import { parseArgs } from 'node:util';
import { fileURLToPath } from 'node:url';
import { inspectWryStartupFile } from './wry-startup-files.ts';
async function bounded(name: string, limit: number) {
  const stat = await lstat(name);
  if (!stat.isFile() || stat.isSymbolicLink() || stat.size > limit) throw new Error();
  const bytes = await readFile(name); if (bytes.length > limit) throw new Error(); return bytes;
}
try {
  const { values: v } = parseArgs({ options: { directory: { type: 'string' }, provenance: { type: 'string' },
    collection: { type: 'string' }, source: { type: 'string' }, exe: { type: 'string' } } });
  if (!v.directory || !v.provenance || !v.collection || !v.source || !v.exe || !/^[0-9a-f]{40}$/.test(v.source)) throw new Error();
  const rawPins = await bounded(fileURLToPath(new URL('../../../tools/wry-startup-trace/pins.json', import.meta.url)), 8192);
  if (createHash('sha256').update(rawPins).digest('hex') !== '250c3047dccf1b444cc6c727b6b47835b1cbef278d1fd09bd102ec260db772ec') throw new Error();
  const pins = JSON.parse(rawPins.toString('utf8')) as Record<string, unknown>;
  const rawProvenance = (await bounded(v.provenance, 8192)).toString('utf8');
  const provenance: unknown = JSON.parse(rawProvenance);
  if (JSON.stringify(provenance) !== rawProvenance) throw new Error();
  const exeSha256 = createHash('sha256').update(await bounded(v.exe, 64 * 1024 * 1024)).digest('hex');
  const expected = { variant: pins.variant, upstreamSha256: pins.upstreamSha256, replacementsSha256: pins.replacementsSha256,
    codeSha256: pins.codeSha256, postimage: pins.postimage, lockBeforeSha256: pins.lockBeforeSha256,
    lockAfterSha256: pins.lockAfterSha256, pumpUpstreamSha256: pins.pumpUpstreamSha256,
    pumpFunctionSha256: pins.pumpFunctionSha256, pumpCodeSha256: pins.pumpCodeSha256, pumpLicenseSha256: pins.pumpLicenseSha256,
    macroUpstreamSha256: pins.macroUpstreamSha256, macroFunctionSha256: pins.macroFunctionSha256, scriptCodeSha256: pins.scriptCodeSha256,
    pinsSha256: createHash('sha256').update(rawPins).digest('hex'), sourceCommit: v.source, exeSha256 };
  function same(a: unknown, b: unknown): boolean {
    if (!a || !b || typeof a !== 'object' || typeof b !== 'object') return a === b;
    if (Array.isArray(a) || Array.isArray(b)) return false;
    const x = a as Record<string, unknown>; const y = b as Record<string, unknown>;
    return Object.keys(x).length === Object.keys(y).length && Object.keys(y).every(k => Object.hasOwn(x, k) && same(x[k], y[k]));
  }
  if (!same(provenance, expected)) throw new Error();
  const collection: unknown = JSON.parse((await bounded(v.collection, 65536)).toString('utf8'));
  const startup = await inspectWryStartupFile(v.directory, collection); const complete = startup.phase !== 'diagnostic-incomplete';
  console.log(JSON.stringify({ code: complete ? 'WRY_STARTUP_COMPLETE' : 'WRY_STARTUP_INCOMPLETE',
    diagnostic: complete ? 'complete' : 'incomplete', provenance: expected, startup }));
  process.exitCode = complete ? 0 : 1;
} catch {
  console.log(JSON.stringify({ code: 'WRY_STARTUP_REJECTED', diagnostic: 'incomplete' })); process.exitCode = 1;
}
