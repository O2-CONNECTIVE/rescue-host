import { test } from 'node:test';
import assert from 'node:assert/strict';
import { classifyWryStartup } from '../native-smoke/wry-startup-stages.ts';

void test('callback未観測でもdispatch入口後のreturn未観測を区別する', () => {
  const trace = fixture(['webview-enter', 'environment-api-start', 'environment-api-return',
    'environment-callback', 'environment-wait-return', 'controller-api-start', 'controller-api-return']);
  const report = classifyWryStartup({ ...trace, variant: 'wry-webview2-stage-2', pumps: {
    environment: { entered: true, phase: 'receiver-ready', iterations: 1, getCalls: 0, getReturns: 0,
      dispatchCalls: 0, dispatchReturns: 0, lastUs: 350, invalid: false },
    controller: { entered: true, phase: 'dispatch', iterations: 1, getCalls: 1, getReturns: 1,
      dispatchCalls: 1, dispatchReturns: 0, lastUs: 900, invalid: false }
  } });
  assert.equal(report.phase, 'controller-callback-not-observed');
  assert.equal(report.pumpHint, 'controller-dispatch-return-not-observed');
});

function fixture(stages: string[]) {
  const empty = { entered: false, phase: 'not-entered', iterations: 0, getCalls: 0, getReturns: 0,
    dispatchCalls: 0, dispatchReturns: 0, lastUs: 0, invalid: false };
  const pump = (scope: string) => stages.includes(`${scope}-wait-return`)
    ? { ...empty, entered: true, phase: 'receiver-ready', iterations: 1, lastUs: stages.indexOf(`${scope}-wait-return`) * 100 - 1 } : { ...empty };
  return { schema: 1, variant: 'wry-webview2-stage-2', purpose: 'diagnostic-hints-only',
    events: stages.map((stage, i) => ({ seq: i + 1, stage, elapsedUs: i * 100 })),
    attempted: stages.length, recorded: stages.length, dropped: 0, sealed: true, inFlight: 0, windowEndUs: 1000,
    pumps: { environment: pump('environment'), controller: pump('controller') } };
}
void test('環境callback済みの後でcontroller callbackを観測しない窓を区別する', () => {
  const report = classifyWryStartup(fixture(['webview-enter', 'environment-api-start',
    'environment-api-return', 'environment-callback', 'environment-wait-return',
    'controller-api-start', 'controller-api-return']));
  assert.equal(report.phase, 'controller-callback-not-observed');
  assert.equal(report.events.length, 7);
});

// 実装後の負例回帰。元Windows障害やCOM callbackの再現ではない。
void test('API returnとcallbackの順を固定せず、環境未完了とcontroller完了を区別する', () => {
  assert.equal(classifyWryStartup(fixture(['webview-enter', 'environment-api-start', 'environment-api-return'])).phase, 'environment-callback-not-observed');
  assert.equal(classifyWryStartup(fixture(['webview-enter', 'environment-api-start', 'environment-callback', 'environment-api-return',
    'environment-wait-return', 'controller-api-start', 'controller-callback', 'controller-api-return', 'controller-wait-return'])).phase, 'controller-wait-returned');
  assert.equal(classifyWryStartup(fixture(['webview-enter', 'environment-reused', 'controller-api-start'])).phase, 'controller-api-return-not-observed');
});
void test('欠測・余剰field・順序違反・終了cutoff以後を不完全とする', () => {
  const good = fixture(['webview-enter', 'environment-api-start']);
  for (const raw of [null, {}, { ...good, dropped: 1 }, { ...good, sealed: false }, { ...good, inFlight: 1 },
    { ...good, recorded: 3 }, { ...good, attempted: 3 }, { ...good, windowEndUs: 1 }, { ...good, endpoint: 'PRIVATE_FIXTURE' },
    fixture(['webview-enter', 'environment-reused', 'environment-api-start']), fixture(['webview-enter', 'controller-api-start']),
    fixture(['webview-enter', 'environment-api-start', 'environment-wait-return']), fixture(['webview-enter', 'webview-enter'])]) {
    const report = classifyWryStartup(raw); assert.equal(report.phase, 'diagnostic-incomplete');
    assert.deepEqual(report.events, []); assert.equal(JSON.stringify(report).includes('PRIVATE_FIXTURE'), false);
  }
});
