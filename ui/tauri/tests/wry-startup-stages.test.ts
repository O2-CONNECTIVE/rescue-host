import { test } from 'node:test';
import assert from 'node:assert/strict';
import { classifyWryStartup } from '../native-smoke/wry-startup-stages.ts';

function fixture(stages: string[]) {
  return { schema: 1, variant: 'wry-webview2-stage-1', purpose: 'diagnostic-hints-only',
    events: stages.map((stage, i) => ({ seq: i + 1, stage, elapsedUs: i * 100 })),
    attempted: stages.length, recorded: stages.length, dropped: 0, sealed: true, inFlight: 0, windowEndUs: 1000 };
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
