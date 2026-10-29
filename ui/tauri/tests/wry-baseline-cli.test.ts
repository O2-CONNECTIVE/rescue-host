import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, writeFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { createHash } from 'node:crypto';
import { spawnSync } from 'node:child_process';

void test('対照CLIはsource/EXEを結び、改変と生の秘密fieldを返さない（後付け回帰）', async () => {
  const folder = await mkdtemp(path.join(tmpdir(), 'rescue-baseline-cli-'));
  try {
    const exe = path.join(folder, 'fixture.bin'); const build = path.join(folder, 'BUILD.json');
    await writeFile(exe, 'NOT_AN_EXECUTABLE');
    const sourceCommit = 'a'.repeat(40); const exeSha256 = createHash('sha256').update('NOT_AN_EXECUTABLE').digest('hex');
    await writeFile(build, JSON.stringify({ schema: 1, kind: 'direct-wry-controller-contrast', sourceCommit,
      exeSha256, featureTreeMatched: true, distribution: false }));
    const collection = { schema: 1, kind: 'direct-wry-controller-contrast', productNativePass: false, sourceCommit, exeSha256,
      fixedInputsVerified: true, wryStartupFileClosed: true, sessionSucceeded: true, normalCloseVerified: true,
      driverStableThroughClose: true, forcedApp: false, forcedChildren: 0, ownerSetRecorded: true, observedChildren: 6,
      observedChildrenExited: true, appExited: true, driversExited: true, listenersAbsent: true, cleanupFailures: 0, secret: 'PRIVATE_FIXTURE' };
    await writeFile(path.join(folder, 'native-result.json'), JSON.stringify(collection));
    await writeFile(path.join(folder, 'wry-startup-report.json'), JSON.stringify({ diagnostic: 'complete',
      provenance: { sourceCommit, exeSha256 }, startup: { phase: 'webview-exit-guard-ok-observed',
        events: [{ stage: 'controller-callback' }, { stage: 'controller-wait-return' }, { stage: 'webview-exit-ok' }] } }));
    await writeFile(path.join(folder, 'baseline-session.json'), JSON.stringify({ schema: 1, kind: 'direct-wry-controller-contrast',
      productNativePass: false, sessionCreated: true, sessionOutcome: 'ok', failurePhase: 'complete', documentChecked: true, browserVersion: '131.0.2903.86' }));
    const invoke = () => spawnSync(process.execPath, ['native-smoke/inspect-wry-baseline.ts', '--directory', folder,
      '--exe', exe, '--build', build, '--source', sourceCommit], { cwd: new URL('..', import.meta.url), encoding: 'utf8', timeout: 5000 });
    const good = invoke(); assert.equal(good.status, 0); assert.equal(good.stderr, '');
    const report = JSON.parse(good.stdout) as { outcome: string; productNativePass: boolean };
    assert.equal(report.outcome, 'contrast-pass'); assert.equal(report.productNativePass, false);
    assert.equal(good.stdout.includes('PRIVATE_FIXTURE'), false); assert.equal(good.stdout.includes(folder), false);
    await writeFile(exe, 'CHANGED'); const changed = invoke(); assert.equal(changed.status, 1);
    assert.equal((JSON.parse(changed.stdout) as { outcome: string }).outcome, 'rejected');
    assert.equal(changed.stdout.includes(folder), false); assert.equal(changed.stderr, '');
  } finally { await rm(folder, { recursive: true, force: true }); }
});
