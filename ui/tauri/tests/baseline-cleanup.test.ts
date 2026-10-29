import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, access, rm, writeFile, readFile } from 'node:fs/promises';
import { spawnSync } from 'node:child_process';
import path from 'node:path';
import { tmpdir } from 'node:os';
import { cleanupBaselineArms, cleanupBaselineShared } from '../native-smoke/baseline-cleanup.ts';

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

void test('共有build一時物は両armの安全確認後だけ削除し、他の資源を残す', async () => {
  const root = await mkdtemp(path.join(tmpdir(), 'baseline-shared-'));
  try {
    await mkdir(path.join(root, 'baseline-project')); await writeFile(path.join(root, 'wry.crate'), 'owned');
    await writeFile(path.join(root, 'unrelated'), 'keep');
    assert.equal(await cleanupBaselineShared(root, { first: 'removed', second: 'unconfirmed' }), 'unconfirmed');
    await access(path.join(root, 'baseline-project'));
    assert.equal(await cleanupBaselineShared(root, { first: 'removed', second: 'not-started' }), 'removed');
    await assert.rejects(access(path.join(root, 'baseline-project')));
    await assert.rejects(access(path.join(root, 'wry.crate')));
    await access(path.join(root, 'unrelated'));
  } finally { await rm(root, { recursive: true, force: true }); }
});

void test('成果回収後のcleanup CLIは同じrunの確認済armを削除し未実施armを区別する', async () => {
  const root = await mkdtemp(path.join(tmpdir(), 'baseline-cleanup-cli-'));
  try {
    await mkdir(path.join(root, 'baseline-tools')); await mkdir(path.join(root, 'baseline-a-evidence'));
    const sourceCommit = 'a'.repeat(40), exeSha256 = 'b'.repeat(64), runId = '12345678-1234-4234-8234-123456789abc';
    await writeFile(path.join(root, 'baseline-build.json'), JSON.stringify({ schema: 1, kind: 'direct-wry-controller-contrast', featureTreeMatched: true, distribution: false, sourceCommit, exeSha256 }));
    await writeFile(path.join(root, 'baseline-sequential.json'), JSON.stringify({ schema: 1, purpose: 'diagnostic-only', sourceCommit, exeSha256, runId, first: 'failed', second: 'not-started', cleanup: 'confirmed', secondCleanup: 'not-started' }));
    await writeFile(path.join(root, 'baseline-a-evidence/native-result.json'), JSON.stringify({ schema: 1, kind: 'direct-wry-controller-contrast', fixedInputsVerified: true,
      mode: 'with-html', sourceCommit, exeSha256, runId, ownerSetRecorded: true, observedChildren: 7, observedChildrenExited: true, appExited: true, driversExited: true, listenersAbsent: true, cleanupFailures: 0 }));
    const run = spawnSync(process.execPath, ['native-smoke/cleanup-baseline-sequential.ts', root], { encoding: 'utf8' });
    assert.equal(run.status, 0);
    assert.deepEqual(JSON.parse(await readFile(path.join(root, 'baseline-cleanup.json'), 'utf8')), { schema: 1, first: 'removed', second: 'not-started' });
    await assert.rejects(access(path.join(root, 'baseline-tools')));
  } finally { await rm(root, { recursive: true, force: true }); }
});
