import { test } from 'node:test';
import assert from 'node:assert/strict';
import { captureNavigationSnapshot, classifyNavigationSnapshots } from '../native-smoke/baseline-navigation.ts';
import { mkdtemp, rm, writeFile, access } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';

void test('前後snapshotにある匿名eventだけを因果関係に応じて分類する', () => {
  const before = { schema: 1, phase: 'before', dropped: 0, events: ['navigation-allowed'] };
  const after = { schema: 1, phase: 'after', dropped: 0, events: ['navigation-allowed', 'load-finished'] };
  assert.deepEqual(classifyNavigationSnapshots(before, after), {
    diagnostic: 'incomplete', snapshotState: 'observed', navigationAllowed: 'before-request', navigationDenied: 'unobserved',
    loadStarted: 'unobserved', loadFinished: 'by-after-snapshot',
  });
  assert.equal(classifyNavigationSnapshots(before, { ...after, dropped: 1 }).diagnostic, 'incomplete');
  assert.equal(classifyNavigationSnapshots(before, { ...after, dropped: 1 }).snapshotState, 'invalid');
  assert.equal(classifyNavigationSnapshots(before, { ...after, events: [] }).diagnostic, 'incomplete');
  assert.equal(classifyNavigationSnapshots(null, after).diagnostic, 'incomplete');
  assert.equal(classifyNavigationSnapshots(before, { ...after, events: ['PRIVATE_FIXTURE'] }).diagnostic, 'incomplete');
  assert.equal(JSON.stringify(classifyNavigationSnapshots(before, { ...after, events: ['PRIVATE_FIXTURE'] })).includes('PRIVATE_FIXTURE'), false);
});

void test('同じ拒否callbackで固定HTMLに厳密一致したときだけ匿名分類する', () => {
  const before = { schema: 1, phase: 'before', dropped: 0, events: ['navigation-denied-fixture'] };
  const after = { ...before, phase: 'after' };
  const report = classifyNavigationSnapshots(before, after);
  assert.equal(report.navigationDenied, 'before-request');
  assert.equal(report.fixtureUriMatch, 'before-request');
  assert.equal(report.diagnostic, 'incomplete');
});

void test('専用dirのackを読み古いrequestを再利用しない（後付け回帰）', async () => {
  const dir = await mkdtemp(path.join(tmpdir(), 'baseline-nav-'));
  const worker = setInterval(() => {
    void access(path.join(dir, 'navigation-before.request')).then(() =>
      writeFile(path.join(dir, 'navigation-before.json'), JSON.stringify({ schema: 1, phase: 'before', dropped: 0, events: [] }), { flag: 'wx' })).catch(() => {});
  }, 5);
  try {
    const result = await captureNavigationSnapshot(dir, 'before');
    assert.deepEqual(result, { schema: 1, phase: 'before', dropped: 0, events: [] });
    assert.equal(await captureNavigationSnapshot(dir, 'before'), null);
  } finally { clearInterval(worker); await rm(dir, { recursive: true, force: true }); }
});
