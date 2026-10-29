import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, writeFile, rm, mkdir } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { inspectAppStartupFile, verifyStartupBuild } from '../native-smoke/app-startup-files.ts';

void test('所有trace fileを有限に読み、page-load済みと欠測を匿名reportへ分ける', async () => {
  const directory = await mkdtemp(path.join(os.tmpdir(), 'rescue-startup-reader-'));
  try {
    const missing = await inspectAppStartupFile(directory);
    assert.equal(missing.phase, 'diagnostic-incomplete');
    const stages = ['setup-enter', 'webview-build-start', 'webview-build-ok', 'setup-return', 'first-page-load-finished'];
    await writeFile(path.join(directory, 'app-startup-stages.json'), JSON.stringify({
      schema: 1, variant: 'host-startup-stage-2', purpose: 'diagnostic-hints-only',
      events: stages.map((stage, i) => ({ seq: i + 1, stage, elapsedUs: i * 100 })),
      attempted: 5, recorded: 5, dropped: 0, sealed: true, inFlight: 0, windowEndUs: 1000
    }));
    assert.equal((await inspectAppStartupFile(directory, {
      kind: 'windows-webview2-native-smoke', appStartupFileClosed: true
    })).phase, 'first-page-load-finished');
    assert.equal((await inspectAppStartupFile(directory)).phase, 'diagnostic-incomplete');
    // 完成fileが後から現れても、回収期限失敗を成功にしない。
    assert.equal((await inspectAppStartupFile(directory, {
      kind: 'windows-webview2-native-smoke', appStartupFileClosed: false
    })).phase, 'diagnostic-incomplete');
  } finally { await rm(directory, { recursive: true, force: true }); }
});

void test('途中・過大・重複field・秘密入りtraceは原文を出さずincompleteにする', async () => {
  const directory = await mkdtemp(path.join(os.tmpdir(), 'rescue-startup-invalid-'));
  const filename = path.join(directory, 'app-startup-stages.json');
  try {
    for (const text of ['{', ' '.repeat(2049), '{"schema":1,"schema":1}', '{"secret":"PRIVATE_FIXTURE"}']) {
      await writeFile(filename, text);
      const report = await inspectAppStartupFile(directory, {
        kind: 'windows-webview2-native-smoke', appStartupFileClosed: true
      });
      assert.equal(report.phase, 'diagnostic-incomplete'); assert.deepEqual(report.events, []);
      assert.equal(JSON.stringify(report).includes('PRIVATE_FIXTURE'), false);
    }
    await rm(filename);
    await mkdir(filename);
    assert.equal((await inspectAppStartupFile(directory)).phase, 'diagnostic-incomplete');
  } finally { await rm(directory, { recursive: true, force: true }); }
});

void test('診断variantのsource・feature・hashが一致しないEXEを起動前に拒否する', () => {
  const manifest = { schema: 1, variant: 'host-startup-diagnostic-1', sourceCommit: 'a'.repeat(40),
    features: ['custom-protocol', 'startup-diagnostics'], profile: 'release',
    exeSha256: 'b'.repeat(64), distribution: false };
  assert.equal(verifyStartupBuild(manifest, 'a'.repeat(40), 'b'.repeat(64)), true);
  for (const candidate of [{ ...manifest, features: ['custom-protocol'] },
    { ...manifest, sourceCommit: 'c'.repeat(40) }, { ...manifest, exeSha256: 'c'.repeat(64) },
    { ...manifest, distribution: true }, { ...manifest, secret: 'PRIVATE_FIXTURE' }]) {
    assert.equal(verifyStartupBuild(candidate, 'a'.repeat(40), 'b'.repeat(64)), false);
  }
});
