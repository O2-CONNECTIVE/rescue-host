import { test } from 'node:test';
import assert from 'node:assert/strict';
import { Driver } from '../native-smoke/webdriver.ts';

void test('配布documentの初期about:blankから固定bundled originへの到達を待つ', async () => {
  const { Driver } = await import('../native-smoke/webdriver.ts');
  const documents = [
    { origin: 'null', href: 'about:blank', documentURI: 'about:blank', readyState: 'complete' },
    { origin: 'http://tauri.localhost', href: 'http://tauri.localhost/', documentURI: 'http://tauri.localhost/', readyState: 'complete' }
  ];
  let reads = 0;
  const driver = new Driver('http://127.0.0.1:4444', () => {
    reads += 1;
    assert.ok(documents.length > 0);
    return Promise.resolve(new Response(JSON.stringify({ value: documents.shift() })));
  });
  await driver.waitForBundledDocument();
  assert.equal(reads, 2);
  assert.equal(documents.length, 0);
});

void test('document状態の型不正をbundled到達の成功へ変換しない', async () => {
  const { Driver } = await import('../native-smoke/webdriver.ts');
  const driver = new Driver('http://127.0.0.1:4444', () => Promise.resolve(new Response(JSON.stringify({ value: {
    origin: 'http://tauri.localhost', href: 'http://tauri.localhost/', documentURI: 'http://tauri.localhost/', readyState: ['complete']
  } }))));
  await assert.rejects(driver.waitForBundledDocument(), /NATIVE_ORIGIN_MISMATCH/);
});

const blank = { origin: 'null', href: 'about:blank', documentURI: 'about:blank', readyState: 'complete' };
const bundled = { origin: 'http://tauri.localhost', href: 'http://tauri.localhost/', documentURI: 'http://tauri.localhost/', readyState: 'complete' };
function replay(values: unknown[]) {
  let reads = 0;
  return { reads: () => reads, driver: new Driver('http://127.0.0.1:4444', () => {
    reads += 1;
    assert.ok(values.length > 0, 'fixture exhausted');
    return Promise.resolve(new Response(JSON.stringify({ value: values.shift() })));
  }) };
}

void test('初期blankとbundled loadingだけを待ち、到達前に成功を返さない', async () => {
  const states: string[] = [];
  const run = replay([blank, { ...bundled, readyState: 'loading' }, bundled]);
  await run.driver.waitForBundledDocument((_origin, state) => { states.push(state); });
  assert.deepEqual(states, ['initial', 'bundled-loading', 'bundled-ready']);
  assert.equal(run.reads(), 3);
});

void test('許可外originやsecurity error documentを一度で拒否し、次の正常値で救済しない', async () => {
  const invalid = [
    { ...bundled, origin: 'https://unexpected.invalid', href: 'https://unexpected.invalid/' },
    { ...bundled, origin: 'https://tauri.localhost', href: 'https://tauri.localhost/' },
    { ...bundled, origin: 'http://tauri.localhost:8080', href: 'http://tauri.localhost:8080/' },
    { ...bundled, href: 'http://tauri.localhost.unexpected.invalid/' },
    { ...bundled, href: 'http://user:pass@tauri.localhost/' },
    { ...bundled, documentURI: 'chrome-error://chromewebdata/' },
    { ...bundled, documentURI: 'edge-error://edgewebdata/' },
    { ...blank, href: 'about:srcdoc' }, { ...blank, documentURI: 'about:certerror' },
    { ...blank, href: 'data:text/html,fixture' }, null, {}, { ...bundled, readyState: null }
  ];
  for (const value of invalid) {
    const run = replay([value, bundled]);
    await assert.rejects(run.driver.waitForBundledDocument(), /NATIVE_ORIGIN_MISMATCH/);
    assert.equal(run.reads(), 1);
  }
});

void test('bundled観測後のabout:blank逆戻りとinitial後のremote遷移を拒否する', async () => {
  for (const values of [[{ ...bundled, readyState: 'loading' }, blank, bundled], [blank, { ...bundled, origin: 'https://unexpected.invalid' }, bundled]]) {
    const run = replay(values);
    await assert.rejects(run.driver.waitForBundledDocument(), /NATIVE_ORIGIN_MISMATCH/);
    assert.equal(run.reads(), 2);
  }
});

void test('document取得のdriver拒否を再試行せず失敗のまま返す', async () => {
  let reads = 0;
  const driver = new Driver('http://127.0.0.1:4444', () => {
    reads += 1;
    return Promise.resolve(new Response(JSON.stringify({ value: { error: 'unknown error', message: 'PRIVATE_CERTIFICATE_WARNING' } }), { status: 500 }));
  });
  await assert.rejects(driver.waitForBundledDocument(), /NATIVE_DRIVER_REQUEST_FAILED/);
  assert.equal(reads, 1);
});

void test('monotonic期限までblankのまま、または期限後のbundled応答なら成功にしない', async t => {
  let clock = 0;
  t.mock.method(performance, 'now', () => clock);
  for (const value of [blank, bundled]) {
    clock = 0;
    let reads = 0;
    const driver = new Driver('http://127.0.0.1:4444', () => {
      reads += 1; clock += 10001;
      return Promise.resolve(new Response(JSON.stringify({ value })));
    });
    await assert.rejects(driver.waitForBundledDocument(), /NATIVE_DOCUMENT_NOT_READY/);
    assert.equal(reads, 1);
  }
});

void test('document観測は分類と件数だけを残し、生URLと拒否された例外文を捨てる', async t => {
  const { mkdtemp, readFile, rm } = await import('node:fs/promises');
  const { tmpdir } = await import('node:os');
  const path = await import('node:path');
  const { createNativeObservation } = await import('../native-smoke/observation.ts');
  const directory = await mkdtemp(path.join(tmpdir(), 'native-document-contract-'));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const observation = createNativeObservation(directory);
  const run = replay([blank, bundled]);
  await run.driver.waitForBundledDocument((origin, state) => { observation.origin(origin); observation.documentState(state); });
  observation.documentState('PRIVATE_DOCUMENT_URL');
  observation.fail(new Error('NATIVE_DOCUMENT_NOT_READY'));
  const raw = await readFile(path.join(directory, 'native-observation.json'), 'utf8');
  const saved: unknown = JSON.parse(raw);
  assert.ok(saved !== null && typeof saved === 'object' && 'documentStates' in saved && 'documentObservations' in saved && 'errorCode' in saved);
  assert.deepEqual(saved.documentStates, ['initial', 'bundled-ready', 'rejected']);
  assert.equal(saved.documentObservations, 3);
  assert.equal(saved.errorCode, 'NATIVE_DOCUMENT_NOT_READY');
  assert.equal(raw.includes('PRIVATE_DOCUMENT_URL'), false);
  assert.equal(raw.includes('http://'), false);
});
