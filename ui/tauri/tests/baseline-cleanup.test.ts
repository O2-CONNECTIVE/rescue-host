import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, access, rm } from 'node:fs/promises';
import path from 'node:path';
import { tmpdir } from 'node:os';
import { cleanupBaselineArms } from '../native-smoke/baseline-cleanup.ts';

void test('終了が確認されたarmだけの専用領域を削除し、未確認の領域は残す', async () => {
  const root = await mkdtemp(path.join(tmpdir(), 'baseline-cleanup-'));
  try {
    for (const name of ['baseline-tools', 'baseline-tools-b', 'baseline-a-evidence', 'baseline-b-evidence']) await mkdir(path.join(root, name));
    const expected = { sourceCommit: 'a'.repeat(40), exeSha256: 'b'.repeat(64), runId: '12345678-1234-4234-8234-123456789abc' };
    const clean = { schema: 1, kind: 'direct-wry-controller-contrast', fixedInputsVerified: true, ...expected,
      ownerSetRecorded: true, observedChildren: 7, observedChildrenExited: true, appExited: true, driversExited: true, listenersAbsent: true, cleanupFailures: 0 };
    assert.deepEqual(await cleanupBaselineArms(root, expected, { ...clean, mode: 'with-html' }, { ...clean, mode: 'load-html', appExited: false }),
      { first: 'removed', second: 'unconfirmed' });
    await assert.rejects(access(path.join(root, 'baseline-tools')));
    await access(path.join(root, 'baseline-tools-b'));
    await access(path.join(root, 'baseline-b-evidence'));
  } finally { await rm(root, { recursive: true, force: true }); }
});
