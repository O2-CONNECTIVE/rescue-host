import { test } from 'node:test';
import assert from 'node:assert/strict';
import { classifyAppStartup } from '../native-smoke/app-startup-stages.ts';

void test('同じapp時計の記録終了で戻りmarker未観測の区間を測る', () => {
  const report = classifyAppStartup({ schema: 1, variant: 'host-startup-stage-2',
    purpose: 'diagnostic-hints-only', events: [
      { seq: 1, stage: 'setup-enter', elapsedUs: 10 },
      { seq: 2, stage: 'webview-build-start', elapsedUs: 100000 }
    ], attempted: 2, recorded: 2, dropped: 0, sealed: true, inFlight: 0, windowEndUs: 14100000 });
  assert.equal(report.phase, 'webview-build-not-observed');
  assert.equal(report.windowEndUs, 14100000);
  assert.equal(report.buildReturnUnobservedUs, 14000000);
});

void test('記録終了の欠測・不正な時計・旧版から待ち時間を推定しない', () => {
  const base = fixture(['setup-enter', 'webview-build-start']);
  for (const value of [undefined, true, -1, 1, NaN, '1000', Number.MAX_SAFE_INTEGER + 1]) {
    const report = classifyAppStartup({ ...base, windowEndUs: value });
    assert.equal(report.phase, 'diagnostic-incomplete');
    assert.equal(report.windowEndUs, null); assert.equal(report.buildReturnUnobservedUs, null);
    assert.deepEqual(report.events, []);
  }
  assert.equal(classifyAppStartup({ ...base, variant: 'host-startup-stage-1' }).phase, 'diagnostic-incomplete');
});

void test('完了markerがある場合は戻り未観測の区間を捏造しない', () => {
  const report = classifyAppStartup(fixture(['setup-enter', 'webview-build-start', 'webview-build-ok', 'setup-return', 'first-page-load-finished']));
  assert.equal(report.phase, 'first-page-load-finished');
  assert.equal(report.windowEndUs, 1000); assert.equal(report.buildReturnUnobservedUs, null);
});

function fixture(stages: string[]) {
  return { schema: 1, variant: 'host-startup-stage-2', purpose: 'diagnostic-hints-only',
    events: stages.map((stage, index) => ({ seq: index + 1, stage, elapsedUs: index * 100 })),
    attempted: stages.length, recorded: stages.length, dropped: 0, sealed: true, inFlight: 0, windowEndUs: 1000 };
}
void test('同じ完全観測窓でWebView build前と初回page-load完了後を区別する', () => {
  assert.equal(classifyAppStartup(fixture(['setup-enter', 'webview-build-start'])).phase, 'webview-build-not-observed');
  assert.equal(classifyAppStartup(fixture(['setup-enter', 'webview-build-start', 'webview-build-ok', 'setup-return', 'first-page-load-finished'])).phase, 'first-page-load-finished');
});

void test('build成功はpage-loadやIPC成功へ昇格せず、page-loadとbuild-returnの順を固定しない', () => {
  assert.equal(classifyAppStartup(fixture(['setup-enter', 'webview-build-start', 'webview-build-ok'])).phase, 'webview-built-page-load-not-observed');
  assert.equal(classifyAppStartup(fixture(['setup-enter', 'webview-build-start', 'first-page-load-finished', 'webview-build-ok', 'setup-return'])).phase, 'first-page-load-finished');
  assert.equal(classifyAppStartup(fixture(['setup-enter', 'webview-build-start', 'webview-build-error'])).phase, 'webview-build-error');
});
void test('欠落・未完了・未知情報・順序不整合は原文を返さず観測不完全とする', () => {
  const base = fixture(['setup-enter', 'webview-build-start']);
  for (const value of [null, {}, { ...base, dropped: 1 }, { ...base, sealed: false }, { ...base, inFlight: 1 },
    { ...base, attempted: 3 }, { ...base, recorded: 3 }, { ...base, dropped: false },
    { ...base, url: 'PRIVATE_FIXTURE' }, fixture(['setup-enter', 'setup-return']),
    fixture(['setup-enter', 'webview-build-start', 'webview-build-error', 'webview-build-ok']),
    { ...base, events: [{ seq: 1, stage: 'setup-enter', elapsedUs: 0, title: 'PRIVATE_FIXTURE' }] },
    { ...base, events: [{ seq: 1, stage: 'setup-enter', elapsedUs: -1 }] },
    fixture(['setup-enter', 'setup-enter'])]) {
    const report = classifyAppStartup(value);
    assert.equal(report.phase, 'diagnostic-incomplete'); assert.deepEqual(report.events, []);
    assert.equal(JSON.stringify(report).includes('PRIVATE_FIXTURE'), false);
  }
});
