import { test } from 'node:test';
import assert from 'node:assert/strict';
import { decideBaseline } from '../native-smoke/baseline-decision.ts';
const collection = { schema: 1, kind: 'direct-wry-controller-contrast', productNativePass: false, configurationMatched: true,
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
