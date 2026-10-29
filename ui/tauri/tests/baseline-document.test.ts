import { test } from 'node:test';
import assert from 'node:assert/strict';
import { inspectBaselineDocument } from '../native-smoke/baseline-document.ts';

void test('同じ応答の各項目を固定値との一致だけに変換する', () => {
  assert.deepEqual(inspectBaselineDocument({ href: 'about:blank', uri: 'about:blank', origin: 'null',
    marker: 'Wry creation contrast', tauri: 'undefined' }),
  { href: 'match', uri: 'match', origin: 'match', marker: 'match', tauri: 'match', accepted: true });
});
