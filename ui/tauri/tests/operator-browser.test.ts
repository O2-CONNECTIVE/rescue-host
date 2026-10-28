import { after, test } from 'node:test';
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import type { AddressInfo } from 'node:net';
import { readFile, readdir, mkdir } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import path from 'node:path';
import { chromium, type Page } from 'playwright-core';

declare global { interface Window { operatorCopied?: string[] } }
const root = fileURLToPath(new URL('../operator/dist/', import.meta.url));
const assets = new Set(['index.html', 'THIRD_PARTY_NOTICES.txt', ...(await readdir(path.join(root, 'assets'))).map(name => `assets/${name}`)]);
const csp = "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'none'; base-uri 'none'; form-action 'none'; frame-src 'none'; object-src 'none'";
const server = createServer((request, response) => {
  void (async () => {
    const name = new URL(request.url ?? '/', 'http://127.0.0.1').pathname.slice(1) || 'index.html';
    if (!assets.has(name)) { response.writeHead(404).end(); return; }
    response.writeHead(200, { 'Content-Security-Policy': csp, 'Content-Type': name.endsWith('.js') ? 'text/javascript' : name.endsWith('.css') ? 'text/css' : name.endsWith('.txt') ? 'text/plain' : 'text/html' });
    response.end(await readFile(path.join(root, name)));
  })().catch(() => { response.writeHead(500).end(); });
});
await new Promise<void>((resolve, reject) => { server.once('error', reject); server.listen(0, '127.0.0.1', resolve); });
const browser = await chromium.launch({ channel: 'chrome', headless: true }).catch(async (error: unknown) => {
  await new Promise<void>(resolve => server.close(() => { resolve(); })); throw error;
});
after(async () => { await browser.close(); await new Promise<void>((resolve, reject) => server.close(error => error ? reject(error) : resolve())); });
const url = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;

async function page() {
  const instance = await browser.newPage({ viewport: { width: 1200, height: 1000 } });
  await instance.addInitScript(() => {
    window.operatorCopied = [];
    window.__TAURI__ = { core: { invoke: () => { throw new Error('NATIVE_INVOKE_FORBIDDEN'); } } };
    Object.defineProperty(navigator, 'clipboard', { value: { writeText: (text: string) => {
      window.operatorCopied?.push(text); return Promise.resolve();
    } } });
  });
  await instance.goto(url);
  await instance.getByRole('heading', { name: 'サポート接続を準備する', exact: true }).waitFor();
  return instance;
}
async function receive(instance: Page) {
  await instance.getByRole('button', { name: 'サンプルを入力', exact: true }).click();
  await instance.getByRole('button', { name: '接続内容を確認', exact: true }).click();
  await instance.getByRole('checkbox').check();
}
async function prepared(instance: Page) {
  await receive(instance);
  await instance.getByRole('button', { name: '承認を依頼（サンプル）', exact: true }).click();
  await instance.getByRole('button', { name: '承認応答を再現', exact: true }).click();
  await instance.getByRole('button', { name: '接続情報の準備完了を再現', exact: true }).click();
}
async function screenshot(instance: Page, name: string) {
  if (!process.env.OPERATOR_SCREENSHOT_DIR) return;
  await mkdir(process.env.OPERATOR_SCREENSHOT_DIR, { recursive: true });
  await instance.screenshot({ path: path.join(process.env.OPERATOR_SCREENSHOT_DIR, name), fullPage: true });
}

void test('操作側の確認・承認・コピー・接続・停止要求・終了確認を区別しkeyboard focusを保つ', async () => {
  const instance = await page();
  const errors: string[] = [];
  instance.on('pageerror', error => errors.push(error.message));
  await instance.getByRole('button', { name: 'サンプルを入力', exact: true }).click();
  await instance.getByRole('button', { name: '接続内容を確認', exact: true }).click();
  assert.equal(await instance.locator(':focus').getAttribute('id'), 'review-heading');
  assert.equal(await instance.getByRole('button', { name: '接続情報をコピー（サンプル）', exact: true }).isDisabled(), true);
  await instance.getByRole('checkbox').focus(); await instance.keyboard.press('Space'); await instance.keyboard.press('Tab');
  assert.equal(await instance.locator(':focus').getAttribute('id'), 'operator-action');
  assert.equal(await instance.locator(':focus').evaluate(node => getComputedStyle(node).outlineWidth), '3px');
  await screenshot(instance, 'operator-review-wide.png');
  await instance.keyboard.press('Enter');
  assert.equal(await instance.locator(':focus').getAttribute('id'), 'operator-action');
  assert.equal(await instance.getByRole('heading', { name: '承認を待っています（モック）', exact: true }).count(), 1);
  await instance.getByRole('button', { name: '承認応答を再現', exact: true }).click();
  assert.equal(await instance.getByRole('heading', { name: '承認済み・接続準備中（モック）', exact: true }).count(), 1);
  await instance.getByRole('button', { name: '接続情報の準備完了を再現', exact: true }).click();
  await instance.getByRole('button', { name: '接続情報をコピー（サンプル）', exact: true }).click();
  await instance.getByText('サンプル接続情報をコピーしました。コード・秘密鍵・パスワードは含みません。', { exact: true }).waitFor();
  const copied = await instance.evaluate(() => window.operatorCopied);
  assert.equal(copied?.length, 1); assert.match(copied?.[0] ?? '', /demo-host\.invalid/);
  assert.doesNotMatch(copied?.[0] ?? '', /DEMO-2486|rescue-client-v1|PRIVATE KEY|password/i);
  await instance.getByRole('button', { name: '外部エージェントの接続を再現', exact: true }).click();
  await instance.getByRole('button', { name: '接続を停止（サンプル）', exact: true }).click();
  assert.equal(await instance.getByRole('heading', { name: '停止を要求しました（モック）', exact: true }).count(), 1);
  assert.equal(await instance.getByRole('heading', { name: '終了確認済み（モック）', exact: true }).count(), 0);
  await instance.getByRole('button', { name: '終了確認の応答を再現', exact: true }).click();
  assert.equal(await instance.getByRole('heading', { name: '終了確認済み（モック）', exact: true }).count(), 1);
  assert.equal(await instance.getByRole('region', { name: '接続履歴（メモリのモック）' }).getByText('終了確認済み（モック）', { exact: true }).count(), 1);
  assert.equal(await instance.getByRole('button', { name: '新しい接続の体験へ', exact: true }).isDisabled(), false);
  assert.deepEqual(errors, []); await instance.close();
});

void test('停止未確認・履歴削除・read-only再確認・失効後の禁止を実画面でも保つ', async () => {
  const instance = await page(); await prepared(instance);
  await instance.getByRole('button', { name: '外部エージェントの接続を再現', exact: true }).click();
  await instance.getByRole('button', { name: '接続を停止（サンプル）', exact: true }).click();
  await instance.getByRole('button', { name: '停止未確認を再現', exact: true }).click();
  assert.equal(await instance.getByRole('button', { name: '新しい接続の体験へ', exact: true }).isDisabled(), true);
  await instance.getByRole('button', { name: '診断情報', exact: true }).focus(); await instance.keyboard.press('Enter');
  assert.equal(await instance.getByRole('button', { name: '診断情報', exact: true }).getAttribute('aria-expanded'), 'true');
  await screenshot(instance, 'operator-unconfirmed-wide.png');
  await instance.getByRole('button', { name: 'サンプル履歴をすべて削除', exact: true }).click();
  await instance.getByRole('button', { name: '履歴を削除する（モック）', exact: true }).click();
  assert.equal(await instance.getByRole('button', { name: '新しい接続の体験へ', exact: true }).isDisabled(), true);
  await instance.getByRole('button', { name: '同じ接続の状態を再確認（モック）', exact: true }).click();
  assert.equal(await instance.getByRole('button', { name: '同じ接続の状態を再確認（モック）', exact: true }).isDisabled(), true);
  await instance.getByRole('button', { name: '同じ接続の稼働状態を再現', exact: true }).click();
  assert.equal(await instance.getByRole('heading', { name: '終了を確認できません（モック）', exact: true }).count(), 1);
  await instance.getByRole('button', { name: '期限切れを再現', exact: true }).click();
  assert.equal(await instance.getByRole('heading', { name: '期限切れ・終了未確認（モック）', exact: true }).count(), 1);
  assert.equal(await instance.getByRole('button', { name: '同じ接続の状態を再確認（モック）', exact: true }).count(), 0);
  await instance.getByRole('button', { name: '終了確認の応答を再現', exact: true }).click();
  assert.equal(await instance.getByText('保存されたサンプル履歴はありません。', { exact: true }).count(), 1);
  await instance.getByRole('button', { name: '新しい接続の体験へ', exact: true }).click();
  await prepared(instance);
  await instance.getByRole('button', { name: '期限切れを再現', exact: true }).click();
  await instance.getByRole('button', { name: '31日経過を再現（履歴のみ）', exact: true }).click();
  assert.equal(await instance.getByText('保存されたサンプル履歴はありません。', { exact: true }).count(), 1);
  assert.equal(await instance.getByRole('button', { name: '新しい接続の体験へ', exact: true }).isDisabled(), true);
  await instance.close();
});

void test('入力の破棄とサンプル定義・トレイ・狭幅拡大・色と動きの代替を確認する', async () => {
  const instance = await page();
  await instance.getByLabel('サンプルの接続コード', { exact: true }).fill('DO_NOT_PERSIST_TEST_FIXTURE');
  await instance.getByRole('button', { name: '接続内容を確認', exact: true }).click();
  assert.equal(await instance.locator('#sample-input').inputValue(), '');
  assert.equal(await instance.locator('#sample-input').getAttribute('aria-invalid'), 'true');
  assert.equal(await instance.locator(':focus').getAttribute('id'), 'sample-input');
  assert.doesNotMatch(await instance.locator('body').innerText(), /DO_NOT_PERSIST_TEST_FIXTURE/);
  assert.deepEqual(await instance.evaluate(() => [localStorage.length, sessionStorage.length, location.search, location.hash]), [0, 0, '', '']);
  await instance.getByLabel('受け取った情報の種類', { exact: true }).selectOption('definition');
  await prepared(instance);
  await instance.getByRole('button', { name: '閉じる（トレイのモック）', exact: true }).click();
  assert.equal(await instance.getByRole('heading', { name: 'トレイ格納中（モック）', exact: true }).count(), 1);
  assert.equal(await instance.getByRole('heading', { name: '接続情報の準備完了（モック）', exact: true }).count(), 1);
  assert.equal(await instance.getByRole('heading', { name: '終了確認済み（モック）', exact: true }).count(), 0);
  for (const [width, font] of [[320, '100%'], [1200, '200%'], [320, '200%']] as const) {
    await instance.setViewportSize({ width, height: 1000 });
    await instance.evaluate(value => { document.documentElement.style.fontSize = value; }, font);
    assert.equal(await instance.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
  }
  await instance.emulateMedia({ reducedMotion: 'reduce', forcedColors: 'active' });
  await instance.getByRole('button', { name: '診断情報', exact: true }).click();
  assert.equal(await instance.locator('.status').evaluate(node => getComputedStyle(node).borderStyle), 'solid');
  assert.equal(await instance.locator('.button').first().evaluate(node => getComputedStyle(node).transitionDuration), '0s');
  assert.equal(await instance.locator('svg:not([aria-hidden="true"])').count(), 0);
  assert.doesNotMatch(await instance.locator('body').innerText(), /\p{Extended_Pictographic}/u);
  await instance.emulateMedia({ forcedColors: 'none' });
  await screenshot(instance, 'operator-tray-narrow200.png');
  await instance.getByRole('button', { name: 'ウィンドウを戻す（モック）', exact: true }).click();
  assert.equal(await instance.locator(':focus').getAttribute('id'), 'review-heading');
  await instance.close();
});

void test('トレイからkeyboardで停止要求したら状態見出しへfocusを戻す', async () => {
  const instance = await page(); await prepared(instance);
  await instance.getByRole('button', { name: '閉じる（トレイのモック）', exact: true }).click();
  await instance.getByRole('button', { name: '接続を停止（サンプル）', exact: true }).focus();
  await instance.keyboard.press('Enter');
  assert.equal(await instance.getByRole('heading', { name: '停止を要求しました（モック）', exact: true }).evaluate(node => node === document.activeElement), true);
  assert.equal(await instance.getByRole('heading', { name: 'トレイ格納中（モック）', exact: true }).count(), 1);
  await instance.close();
});

void test('履歴削除をkeyboardでキャンセルしたら確認を開いたボタンへfocusを戻す', async () => {
  const instance = await page(); await prepared(instance);
  await instance.getByRole('button', { name: '接続を停止（サンプル）', exact: true }).click();
  await instance.getByRole('button', { name: '終了確認の応答を再現', exact: true }).click();
  const trigger = instance.getByRole('button', { name: 'サンプル履歴をすべて削除', exact: true });
  await trigger.focus(); await instance.keyboard.press('Enter');
  await instance.getByRole('button', { name: 'キャンセル', exact: true }).focus();
  await instance.keyboard.press('Enter');
  assert.equal(await trigger.evaluate(node => node === document.activeElement), true);
  assert.equal(await instance.getByRole('region', { name: '接続履歴（メモリのモック）' }).getByText('終了確認済み（モック）', { exact: true }).count(), 1);
  await instance.close();
});

void test('通常の確認画面は相手・許可・期限を残し、技術・診断・履歴を詳細へ段階開示する', async () => {
  const instance = await page(); await receive(instance);
  const disclosure = instance.getByRole('button', { name: '詳細を見る', exact: true });
  assert.equal(await disclosure.count(), 1);
  assert.equal(await disclosure.getAttribute('aria-expanded'), 'false');
  assert.equal(await instance.getByText('練習用PC 01（架空）', { exact: true }).isVisible(), true);
  assert.equal(await instance.getByText('指定した1台への SSH 接続・非管理者ユーザー', { exact: true }).isVisible(), true);
  assert.equal(await instance.getByText('承認後15分（サンプル・自動延長なし）', { exact: true }).isVisible(), true);
  assert.equal(await instance.getByRole('heading', { name: 'モックの応答', exact: true }).count(), 0);
  assert.equal(await instance.getByRole('heading', { name: '診断情報', exact: true }).count(), 0);
  assert.equal(await instance.getByRole('region', { name: '接続履歴（メモリのモック）' }).count(), 0);
  await disclosure.focus(); await instance.keyboard.press('Enter');
  assert.equal(await disclosure.getAttribute('aria-expanded'), 'true');
  assert.equal(await instance.getByRole('heading', { name: '診断情報', exact: true }).isVisible(), true);
  await instance.keyboard.press('Enter');
  assert.equal(await disclosure.getAttribute('aria-expanded'), 'false');
  assert.equal(await disclosure.evaluate(node => node === document.activeElement), true);
  await instance.close();
});
