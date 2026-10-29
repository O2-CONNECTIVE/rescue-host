import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, readFile, writeFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { createHash } from 'node:crypto';
import { spawnSync } from 'node:child_process';
import { inspectWryStartupFile } from '../native-smoke/wry-startup-files.ts';

void test('回収証跡と固定来歴を照合し、欠測・秘密field・hash不一致を拒否する', async () => {
  const folder = await mkdtemp(path.join(tmpdir(), 'rescue-wry-cli-'));
  try {
    const exe = path.join(folder, 'fixture.bin'); await writeFile(exe, 'NOT_AN_EXECUTABLE');
    const rawPins = await readFile(new URL('../../../tools/wry-startup-trace/pins.json', import.meta.url));
    const pins = JSON.parse(rawPins.toString('utf8')) as Record<string, unknown>;
    const provenance = { variant: pins.variant, upstreamSha256: pins.upstreamSha256, replacementsSha256: pins.replacementsSha256,
      codeSha256: pins.codeSha256, postimage: pins.postimage, lockBeforeSha256: pins.lockBeforeSha256, lockAfterSha256: pins.lockAfterSha256,
      pumpUpstreamSha256: pins.pumpUpstreamSha256, pumpFunctionSha256: pins.pumpFunctionSha256,
      pumpCodeSha256: pins.pumpCodeSha256, pumpLicenseSha256: pins.pumpLicenseSha256,
      macroUpstreamSha256: pins.macroUpstreamSha256, macroFunctionSha256: pins.macroFunctionSha256, scriptCodeSha256: pins.scriptCodeSha256,
      pinsSha256: createHash('sha256').update(rawPins).digest('hex'), sourceCommit: 'a'.repeat(40),
      exeSha256: createHash('sha256').update('NOT_AN_EXECUTABLE').digest('hex') };
    const native = { kind: 'windows-webview2-native-smoke', wryStartupFileClosed: true };
    await writeFile(path.join(folder, 'provenance.json'), JSON.stringify(provenance));
    await writeFile(path.join(folder, 'collection.json'), JSON.stringify(native));
    const empty = { entered: false, phase: 'not-entered', iterations: 0, getCalls: 0, getReturns: 0,
      dispatchCalls: 0, dispatchReturns: 0, lastUs: 0, invalid: false };
    const trace = { schema: 1, variant: 'wry-webview2-stage-3', purpose: 'diagnostic-hints-only',
      events: [{ seq: 1, stage: 'webview-enter', elapsedUs: 0 }], attempted: 1, recorded: 1, dropped: 0, sealed: true, inFlight: 0, windowEndUs: 100,
      pumps: { environment: empty, controller: empty, script: empty },
      scripts: { calls: 0, apiReturns: 0, callbacks: 0, sends: 0, returns: 0, pumpOrdinal: 0,
        startedUs: null, apiStartUs: null, apiReturnUs: null, callbackUs: null, sentUs: null,
        returnUs: null, result: 'idle', callbackOk: null, invalid: false } };
    await writeFile(path.join(folder, 'wry-startup-stages.json'), JSON.stringify(trace));
    assert.equal((await inspectWryStartupFile(folder, {})).phase, 'diagnostic-incomplete');
    const invoke = () => spawnSync(process.execPath, ['native-smoke/inspect-wry-startup.ts', '--directory', folder,
      '--provenance', path.join(folder, 'provenance.json'), '--collection', path.join(folder, 'collection.json'), '--exe', exe, '--source', 'a'.repeat(40)],
    { cwd: new URL('..', import.meta.url), encoding: 'utf8', timeout: 5000 });
    const good = invoke(); assert.equal(good.status, 0); assert.equal(good.stderr, '');
    assert.equal((JSON.parse(good.stdout) as { startup: { phase: string } }).startup.phase, 'environment-api-start-not-observed');
    // 対照は製品native合格を名乗らず、別kindで同じ記録来歴を検証する。
    await writeFile(path.join(folder, 'collection.json'), JSON.stringify({ kind: 'direct-wry-controller-contrast', schema: 1,
      productNativePass: false, wryStartupFileClosed: true }));
    assert.equal(invoke().status, 0);
    await writeFile(path.join(folder, 'wry-startup-stages.json'), JSON.stringify({ ...trace, url: 'PRIVATE_FIXTURE' }));
    const bad = invoke(); assert.equal(bad.status, 1); assert.equal(bad.stdout.includes('PRIVATE_FIXTURE'), false);
    await writeFile(path.join(folder, 'provenance.json'), JSON.stringify({ ...provenance, exeSha256: 'b'.repeat(64) }));
    assert.equal(invoke().status, 1);
    await writeFile(path.join(folder, 'wry-startup-stages.json'), ' '.repeat(2049));
    assert.equal((await inspectWryStartupFile(folder, native)).phase, 'diagnostic-incomplete');
  } finally { await rm(folder, { recursive: true, force: true }); }
});
