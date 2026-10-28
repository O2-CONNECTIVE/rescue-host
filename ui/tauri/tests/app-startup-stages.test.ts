import { test } from 'node:test';
import assert from 'node:assert/strict';
import { classifyAppStartup } from '../native-smoke/app-startup-stages.ts';

function fixture(stages: string[]) {
  return { schema: 1, variant: 'host-startup-stage-1', purpose: 'diagnostic-hints-only',
    events: stages.map((stage, index) => ({ seq: index + 1, stage, elapsedUs: index * 100 })),
    attempted: stages.length, recorded: stages.length, dropped: 0, sealed: true, inFlight: 0 };
}
void test('同じ完全観測窓でWebView build前と初回page-load完了後を区別する', () => {
  assert.equal(classifyAppStartup(fixture(['setup-enter', 'webview-build-start'])).phase, 'webview-build-not-observed');
  assert.equal(classifyAppStartup(fixture(['setup-enter', 'webview-build-start', 'webview-build-ok', 'setup-return', 'first-page-load-finished'])).phase, 'first-page-load-finished');
});
