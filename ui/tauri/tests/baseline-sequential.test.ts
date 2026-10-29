import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mayStartSecond } from '../native-smoke/baseline-sequential.ts';

void test('先行armの合否と独立して所有資源の終了確認が揃う場合だけ後続を開始する', () => {
  const clean = { schema: 1, kind: 'direct-wry-controller-contrast', fixedInputsVerified: true,
    ownerSetRecorded: true, observedChildren: 7, observedChildrenExited: true, appExited: true,
    driversExited: true, listenersAbsent: true, cleanupFailures: 0, normalCloseVerified: false, forcedApp: true };
  assert.equal(mayStartSecond(clean), true);
  for (const change of [{ ownerSetRecorded: false }, { observedChildren: 0 }, { appExited: false },
    { observedChildrenExited: false }, { driversExited: false }, { listenersAbsent: false }, { cleanupFailures: 1 },
    { fixedInputsVerified: false }, { kind: 'other' }]) assert.equal(mayStartSecond({ ...clean, ...change }), false);
  assert.equal(mayStartSecond(null), false);
  const missing = { ...clean } as Partial<typeof clean>; delete missing.cleanupFailures;
  assert.equal(mayStartSecond(missing), false);
});
