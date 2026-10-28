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
