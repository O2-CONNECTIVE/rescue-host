import { test } from 'node:test';
import assert from 'node:assert/strict';
import { inspectBaselineDocument } from '../native-smoke/baseline-document.ts';
import { createNativeObservation } from '../native-smoke/observation.ts';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';

void test('同一応答の要素不在と文字列不一致と読込状態を匿名分類する', () => {
  const base = { href: 'about:blank', uri: 'about:blank', origin: 'null', tauri: 'undefined' };
  const missing = inspectBaselineDocument({ ...base, marker: null, markerPresent: false, readyState: 'loading' });
  assert.equal(missing.markerDetail, 'absent');
  assert.equal(missing.readyState, 'loading');
  assert.equal(missing.accepted, false);
  const wrong = inspectBaselineDocument({ ...base, marker: 'PRIVATE_FIXTURE', markerPresent: true, readyState: 'interactive' });
  assert.equal(wrong.markerDetail, 'text-mismatch');
  assert.equal(wrong.readyState, 'interactive');
  assert.equal(JSON.stringify(wrong).includes('PRIVATE_FIXTURE'), false);
  const match = inspectBaselineDocument({ ...base, marker: 'Wry creation contrast', markerPresent: true, readyState: 'complete' });
  assert.equal(match.markerDetail, 'match');
  assert.equal(match.readyState, 'complete');
  assert.equal(match.accepted, true);
  const unknown = inspectBaselineDocument({ ...base, marker: 4, readyState: 'PRIVATE_FIXTURE' });
  assert.equal(unknown.markerDetail, 'unknown');
  assert.equal(unknown.readyState, 'unknown');
  assert.equal(inspectBaselineDocument({ ...base, marker: 'PRIVATE_FIXTURE', markerPresent: false }).markerDetail, 'unknown');
  assert.equal(inspectBaselineDocument({ ...base, marker: null, markerPresent: true }).markerDetail, 'unknown');
});

void test('markerが存在しないと申告した矛盾応答は成功にしない', () => {
  const result = inspectBaselineDocument({ href: 'about:blank', uri: 'about:blank', origin: 'null', marker: 'Wry creation contrast', markerPresent: false, tauri: 'undefined' });
  assert.equal(result.accepted, false);
});

void test('同じ応答の各項目を固定値との一致だけに変換する', () => {
  assert.deepEqual(inspectBaselineDocument({ href: 'about:blank', uri: 'about:blank', origin: 'null',
    marker: 'Wry creation contrast', tauri: 'undefined' }),
  { href: 'match', uri: 'match', origin: 'match', marker: 'match', tauri: 'match', accepted: false, markerDetail: 'unknown', readyState: 'unknown' });
});
void test('固定document拒否を未知例外と混同せず記録する', () => {
  const dir = mkdtempSync(path.join(tmpdir(), 'baseline-rejection-'));
  try {
    createNativeObservation(dir).fail(new Error('NATIVE_BASELINE_DOCUMENT_REJECTED'));
    const result = JSON.parse(readFileSync(path.join(dir, 'native-observation.json'), 'utf8')) as { errorCode: string };
    assert.equal(result.errorCode, 'NATIVE_BASELINE_DOCUMENT_REJECTED');
  } finally { rmSync(dir, { recursive: true, force: true }); }
});
void test('不一致と未知を区別し、原値を返さず受入条件を緩めない（後付け回帰）', () => {
  const privateValue = 'PRIVATE_FIXTURE';
  const bad = inspectBaselineDocument({ href: privateValue, uri: 4, origin: 'null', marker: null });
  assert.deepEqual(bad, { href: 'mismatch', uri: 'unknown', origin: 'match', marker: 'mismatch', tauri: 'unknown', accepted: false, markerDetail: 'unknown', readyState: 'unknown' });
  assert.equal(JSON.stringify(bad).includes(privateValue), false);
  assert.equal(inspectBaselineDocument(null).accepted, false);
  assert.equal(inspectBaselineDocument({ href: 'about:blank', uri: 'about:blank', origin: 'null',
    marker: 'Wry creation contrast', tauri: 'PRIVATE_FIXTURE' }).accepted, false);
});
