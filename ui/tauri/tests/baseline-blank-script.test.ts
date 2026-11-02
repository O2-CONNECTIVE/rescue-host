import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';
import { inspectBaselineDocument } from '../native-smoke/baseline-document.ts';

void test('固定スクリプトがblank内に非通信のmarkerを作り検査を通す', () => {
  const source = readFileSync('../../tools/wry-creation-baseline/main.rs', 'utf8');
  const literal = source.match(/const BLANK_SCRIPT: &str = ("(?:[^"\\]|\\.)*");/u)?.[1];
  assert.ok(literal);
  const script = JSON.parse(literal) as string;
  const head: Record<string, string>[] = [], body: Record<string, string>[] = [];
  const document = { createElement: (kind: string) => ({ kind }), head: { appendChild: (el: Record<string, string>) => head.push(el) }, body: { appendChild: (el: Record<string, string>) => body.push(el) } };
  vm.runInNewContext(script, { document }, { timeout: 100 });
  assert.equal(head.length, 1);
  assert.equal(head[0].content, "default-src 'none'; base-uri 'none'; form-action 'none'");
  assert.equal(body.length, 1);
  assert.equal(body[0].id, 'wry-creation-contrast');
  assert.equal(body[0].textContent, 'Wry creation contrast');
  const result = inspectBaselineDocument({ href: 'about:blank', uri: 'about:blank', origin: 'null', marker: body[0].textContent, markerPresent: true, tauri: 'undefined', readyState: 'complete' });
  assert.equal(result.accepted, true);
  assert.equal(inspectBaselineDocument({ href: 'about:blank', uri: 'about:blank', origin: 'null', marker: null, markerPresent: false, tauri: 'undefined' }).accepted, false);
});
