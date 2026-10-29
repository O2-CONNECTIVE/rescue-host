import { test } from 'node:test';
import assert from 'node:assert/strict';
import { classifyNavigationSnapshots } from '../native-smoke/baseline-navigation.ts';

void test('前後snapshotにある匿名eventだけを因果関係に応じて分類する', () => {
  const before = { schema: 1, phase: 'before', dropped: 0, events: ['navigation-allowed'] };
  const after = { schema: 1, phase: 'after', dropped: 0, events: ['navigation-allowed', 'load-finished'] };
  assert.deepEqual(classifyNavigationSnapshots(before, after), {
    diagnostic: 'complete', navigationAllowed: 'before-request', navigationDenied: 'unobserved',
    loadStarted: 'unobserved', loadFinished: 'between-snapshots',
  });
  assert.equal(classifyNavigationSnapshots(before, { ...after, dropped: 1 }).diagnostic, 'incomplete');
  assert.equal(classifyNavigationSnapshots(before, { ...after, events: [] }).diagnostic, 'incomplete');
  assert.equal(classifyNavigationSnapshots(null, after).diagnostic, 'incomplete');
  assert.equal(classifyNavigationSnapshots(before, { ...after, events: ['PRIVATE_FIXTURE'] }).diagnostic, 'incomplete');
  assert.equal(JSON.stringify(classifyNavigationSnapshots(before, { ...after, events: ['PRIVATE_FIXTURE'] })).includes('PRIVATE_FIXTURE'), false);
});
