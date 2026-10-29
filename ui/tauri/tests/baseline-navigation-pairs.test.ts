import { test } from 'node:test';
import assert from 'node:assert/strict';
import { classifyNavigationPairs } from '../native-smoke/baseline-navigation-pairs.ts';

void test('同じ匿名slotの開始と完了だけを対応付ける', () => {
  const start = { slot: 1, kind: 'start', decision: 'denied', cancel: 'applied', uri: 'other' };
  const finish = { slot: 1, kind: 'complete', success: 'failure' };
  const before = { schema: 1, phase: 'before', dropped: 0, pairs: [start, finish] };
  const after = { ...before, phase: 'after' };
  assert.deepEqual(classifyNavigationPairs(before, after), { diagnostic: 'incomplete', snapshotState: 'observed',
    pairs: [{ slot: 1, starts: [{ decision: 'denied', cancel: 'applied', uri: 'other', observed: 'before-request' }],
      completion: { success: 'failure', observed: 'before-request' } }] });
  assert.equal(classifyNavigationPairs(before, { ...after, pairs: [{ ...finish, slot: 2 }] }).snapshotState, 'invalid');
  assert.equal(classifyNavigationPairs(before, { ...after, dropped: 1 }).snapshotState, 'invalid');
  assert.equal(classifyNavigationPairs(null, after).snapshotState, 'invalid');
  assert.equal(JSON.stringify(classifyNavigationPairs(before, { ...after, pairs: [{ ...start, uri: 'PRIVATE_FIXTURE' }] })).includes('PRIVATE_FIXTURE'), false);
});
