import { test } from 'node:test';
import assert from 'node:assert/strict';
import { decideBaseline } from '../native-smoke/baseline-decision.ts';
const collection = { schema: 1, kind: 'direct-wry-controller-contrast', productNativePass: false, fixedInputsVerified: true,
  wryStartupFileClosed: true, sessionSucceeded: true, normalCloseVerified: true, forcedApp: false, forcedChildren: 0,
  ownerSetRecorded: true, observedChildren: 6, observedChildrenExited: true, appExited: true,
  driversExited: true, listenersAbsent: true, cleanupFailures: 0 };
const wry = { diagnostic: 'complete', startup: { phase: 'webview-exit-guard-ok-observed',
  events: [{ stage: 'controller-callback' }, { stage: 'controller-wait-return' }, { stage: 'webview-exit-ok' }] } };
const session = { schema: 1, kind: 'direct-wry-controller-contrast', productNativePass: false,
  sessionCreated: true, documentChecked: true, browserVersion: '131.0.2903.86' };
void test('直接Wryのsession・退出・通常closeは対照成功だけを返す', () => {
  const r = decideBaseline(collection, wry, session);
  assert.equal(r.contrastPassed, true);
  assert.equal(r.productNativePass, false);
  assert.equal(r.outcome, 'contrast-pass');
});
void test('強制cleanup・欠測・設定不一致・不確定な子終了を対照成功にしない（後付け回帰）', () => {
  for (const delta of [{ normalCloseVerified: false }, { forcedApp: true }, { forcedChildren: 1 },
    { cleanupFailures: 1 }, { ownerSetRecorded: false }, { observedChildren: 0 }, { appExited: false },
    { observedChildrenExited: false }, { driversExited: false }, { listenersAbsent: false },
    { fixedInputsVerified: false }, { wryStartupFileClosed: false }, { productNativePass: true }]) {
    assert.equal(decideBaseline({ ...collection, ...delta }, wry, session).contrastPassed, false);
  }
  assert.equal(decideBaseline(collection, { diagnostic: 'incomplete' }, session).diagnostic, 'incomplete');
  assert.equal(decideBaseline(collection, wry, { ...session, browserVersion: '156.0.0.0' }).contrastPassed, false);
});
void test('callback未観測と退出後timeoutを分離し、原因確定としない（後付け回帰）', () => {
  const failed = { ...collection, sessionSucceeded: false, normalCloseVerified: false };
  const pending = decideBaseline(failed, { diagnostic: 'complete', startup: { phase: 'controller-callback-not-observed',
    events: [{ stage: 'controller-api-return' }] } }, null);
  assert.equal(pending.outcome, 'controller-callback-unobserved');
  assert.equal(pending.rootCauseProven, false);
  assert.equal(pending.contrastPassed, false);
  assert.equal(decideBaseline(failed, wry, null).outcome, 'session-after-wry-exit');
  assert.equal(decideBaseline({ ...failed, ownerSetRecorded: false }, wry, null).nextAction, 'repair-ownership-observation');
});
