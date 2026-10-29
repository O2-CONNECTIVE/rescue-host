import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, access, rm, writeFile, readFile } from 'node:fs/promises';
import { spawnSync } from 'node:child_process';
import path from 'node:path';
import { tmpdir } from 'node:os';
import { cleanupBaselineArms, cleanupBaselineShared } from '../native-smoke/baseline-cleanup.ts';

void test('正例のcleanupは同じmodeと所有終了の確認が必要', async () => {
  const root = await mkdtemp(path.join(tmpdir(), 'baseline-positive-'));
  try {
    await mkdir(path.join(root, 'baseline-tools')); await mkdir(path.join(root, 'baseline-a-evidence'));
    const expected = { sourceCommit: 'a'.repeat(40), exeSha256: 'b'.repeat(64), runId: '12345678-1234-4234-8234-123456789abc' };
    const report = { schema: 1, kind: 'direct-wry-controller-contrast', fixedInputsVerified: true, mode: 'blank-script', ...expected,
      ownerSetRecorded: true, observedChildren: 7, observedChildrenExited: true, appExited: true, driversExited: true, listenersAbsent: true, cleanupFailures: 0 };
    assert.equal((await cleanupBaselineArms(root, expected, { ...report, appExited: false }, null, 'blank-script')).first, 'unconfirmed');
    assert.equal((await cleanupBaselineArms(root, expected, report, null, 'blank-script')).first, 'removed');
  } finally { await rm(root, { recursive: true, force: true }); }
});

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
  const parent = await mkdtemp(path.join(tmpdir(), 'baseline-cleanup-cli-'));
  try {
    const sourceCommit = 'a'.repeat(40), exeSha256 = 'b'.repeat(64), runId = '12345678-1234-4234-8234-123456789abc';
    const root = path.join(parent, `rescue-wry-${runId}`); await mkdir(root);
    await writeFile(path.join(root, 'owner.json'), JSON.stringify({ schema: 1, purpose: 'wry-sequential-owned', runId }));
    await mkdir(path.join(root, 'baseline-tools')); await mkdir(path.join(root, 'baseline-a-evidence'));
    await writeFile(path.join(root, 'baseline-build.json'), JSON.stringify({ schema: 1, kind: 'direct-wry-controller-contrast', featureTreeMatched: true, distribution: false, sourceCommit, exeSha256 }));
    await writeFile(path.join(root, 'baseline-sequential.json'), JSON.stringify({ schema: 1, purpose: 'diagnostic-only', sourceCommit, exeSha256, runId, first: 'failed', second: 'not-started', cleanup: 'confirmed', secondCleanup: 'not-started' }));
    await writeFile(path.join(root, 'baseline-a-evidence/native-result.json'), JSON.stringify({ schema: 1, kind: 'direct-wry-controller-contrast', fixedInputsVerified: true,
      mode: 'with-html', sourceCommit, exeSha256, runId, ownerSetRecorded: true, observedChildren: 7, observedChildrenExited: true, appExited: true, driversExited: true, listenersAbsent: true, cleanupFailures: 0 }));
    const run = spawnSync(process.execPath, ['native-smoke/cleanup-baseline-sequential.ts', root], { encoding: 'utf8', env: { ...process.env, RUNNER_TEMP: parent } });
    assert.equal(run.status, 0);
    assert.deepEqual(JSON.parse(await readFile(path.join(parent, `baseline-cleanup-${runId}.json`), 'utf8')), { schema: 1, first: 'removed', second: 'not-started', shared: 'removed', root: 'removed' });
    await assert.rejects(access(root));
  } finally { await rm(parent, { recursive: true, force: true }); }
});

void test('正例cleanup CLIは同一runの正例だけを削除し未確認なら保持する', async () => {
  for (const confirmed of [false, true]) {
    const parent = await mkdtemp(path.join(tmpdir(), 'baseline-positive-cli-'));
    try {
      const sourceCommit = 'a'.repeat(40), exeSha256 = 'b'.repeat(64), runId = '12345678-1234-4234-8234-123456789abc';
      const root = path.join(parent, `rescue-wry-${runId}`); await mkdir(root);
      await writeFile(path.join(root, 'owner.json'), JSON.stringify({ schema: 1, purpose: 'wry-sequential-owned', runId }));
      await mkdir(path.join(root, 'baseline-tools')); await mkdir(path.join(root, 'baseline-a-evidence'));
      await writeFile(path.join(root, 'baseline-build.json'), JSON.stringify({ schema: 1, kind: 'direct-wry-controller-contrast', featureTreeMatched: true, distribution: false, sourceCommit, exeSha256 }));
      await writeFile(path.join(root, 'baseline-sequential.json'), JSON.stringify({ schema: 1, purpose: 'diagnostic-only', sourceCommit, exeSha256, runId, firstMode: 'blank-script', first: 'failed', second: 'not-started', cleanup: confirmed ? 'confirmed' : 'unconfirmed', secondCleanup: 'not-started' }));
      await writeFile(path.join(root, 'baseline-a-evidence/native-result.json'), JSON.stringify({ schema: 1, kind: 'direct-wry-controller-contrast', fixedInputsVerified: true, mode: 'blank-script', sourceCommit, exeSha256, runId, ownerSetRecorded: true, observedChildren: 7, observedChildrenExited: true, appExited: true, driversExited: true, listenersAbsent: true, cleanupFailures: 0 }));
      const run = spawnSync(process.execPath, ['native-smoke/cleanup-baseline-sequential.ts', root], { encoding: 'utf8', env: { ...process.env, RUNNER_TEMP: parent } });
      assert.equal(run.status, confirmed ? 0 : 1);
      if (confirmed) await assert.rejects(access(root)); else await access(path.join(root, 'baseline-tools'));
    } finally { await rm(parent, { recursive: true, force: true }); }
  }
});

void test('所有markerが違う領域は固定名があっても削除しない', async () => {
  const parent = await mkdtemp(path.join(tmpdir(), 'baseline-owner-'));
  try {
    const runId = '12345678-1234-4234-8234-123456789abc', root = path.join(parent, `rescue-wry-${runId}`);
    await mkdir(root); await mkdir(path.join(root, 'baseline-tools'));
    await writeFile(path.join(root, 'owner.json'), JSON.stringify({ schema: 1, purpose: 'wry-sequential-owned', runId: 'other' }));
    const run = spawnSync(process.execPath, ['native-smoke/cleanup-baseline-sequential.ts', root], { encoding: 'utf8', env: { ...process.env, RUNNER_TEMP: parent } });
    assert.equal(run.status, 1);
    await access(path.join(root, 'baseline-tools'));
  } finally { await rm(parent, { recursive: true, force: true }); }
});
