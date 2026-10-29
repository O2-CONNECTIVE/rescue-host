import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mayStartSecond } from '../native-smoke/baseline-sequential.ts';
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { spawnSync } from 'node:child_process';
import { tmpdir } from 'node:os';
import path from 'node:path';

void test('先行armの合否と独立して所有資源の終了確認が揃う場合だけ後続を開始する', () => {
  const expected = { sourceCommit: 'a'.repeat(40), exeSha256: 'b'.repeat(64), runId: '12345678-1234-4234-8234-123456789abc' };
  const clean = { schema: 1, kind: 'direct-wry-controller-contrast', fixedInputsVerified: true,
    mode: 'with-html', ...expected,
    ownerSetRecorded: true, observedChildren: 7, observedChildrenExited: true, appExited: true,
    driversExited: true, listenersAbsent: true, cleanupFailures: 0, normalCloseVerified: false, forcedApp: true };
  assert.equal(mayStartSecond(clean, expected), true);
  for (const change of [{ ownerSetRecorded: false }, { observedChildren: 0 }, { appExited: false },
    { observedChildrenExited: false }, { driversExited: false }, { listenersAbsent: false }, { cleanupFailures: 1 },
    { fixedInputsVerified: false }, { kind: 'other' }, { mode: 'load-html' }, { sourceCommit: 'bad' },
    { exeSha256: 'bad' }, { sourceCommit: 'c'.repeat(40) }, { exeSha256: 'd'.repeat(64) },
    { runId: '12345678-1234-4234-8234-123456789abd' }]) assert.equal(mayStartSecond({ ...clean, ...change }, expected), false);
  assert.equal(mayStartSecond(null, expected), false);
  const missing = { ...clean } as Partial<typeof clean>; delete missing.cleanupFailures;
  assert.equal(mayStartSecond(missing, expected), false);
});

void test('開始gate CLIは欠落や秘密を含む入力を露出せず拒否する', () => {
  const dir = mkdtempSync(path.join(tmpdir(), 'sequential-gate-'));
  try {
    const input = path.join(dir, 'result.json');
    writeFileSync(input, JSON.stringify({ secret: 'PRIVATE_FIXTURE' }));
    const run = spawnSync(process.execPath, ['native-smoke/check-baseline-cleanup.ts', input], { encoding: 'utf8' });
    assert.equal(run.status, 1);
    assert.equal(run.stdout.trim(), 'BASELINE_CLEANUP_UNCONFIRMED');
    assert.equal((run.stdout + run.stderr).includes('PRIVATE_FIXTURE'), false);
  } finally { rmSync(dir, { recursive: true, force: true }); }
});
