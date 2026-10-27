import { test, after } from 'node:test';
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { readFile, mkdir } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import path from 'node:path';
import { chromium } from 'playwright-core';

const root = fileURLToPath(new URL('../', import.meta.url));
const config = JSON.parse(await readFile(path.join(root, 'src-tauri/tauri.conf.json')));
const assets = new Set(['index.html', 'tokens.css', 'components.css', 'app.css', 'app.mjs', 'controller.mjs']);
const server = createServer(async (request, response) => {
  const name = new URL(request.url, 'http://127.0.0.1').pathname.slice(1) || 'index.html';
  if (!assets.has(name)) { response.writeHead(404).end(); return; }
  const type = name.endsWith('.css') ? 'text/css' : name.endsWith('.mjs') ? 'text/javascript' : 'text/html';
  response.writeHead(200, { 'Content-Type': type, 'Content-Security-Policy': config.app.security.csp });
  response.end(await readFile(path.join(root, 'dist', name)));
});
await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
const browser = await chromium.launch({ channel: 'chrome', headless: true });
const url = `http://127.0.0.1:${server.address().port}`;
after(async () => { await browser.close(); await new Promise(resolve => server.close(resolve)); });

test('browserではRust未取得を明示し、keyboard・focus・拡大でも無効操作と診断を保つ', async () => {
  const page = await browser.newPage({ viewport: { width: 1200, height: 1000 } });
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  await page.goto(url);
  await page.waitForFunction(() => document.querySelector('#diagnostic').textContent === 'BRIDGE_MISSING');
  assert.match(await page.locator('#status-description').innerText(), /ブラウザー表示/);
  assert.equal(await page.getByRole('button', { name: 'サポートを開始' }).isDisabled(), true);
  assert.equal(await page.getByRole('button', { name: 'すべて停止' }).isDisabled(), true);
  await page.keyboard.press('Tab');
  assert.equal(await page.locator(':focus').evaluate(node => node.tagName), 'SUMMARY');
  await page.keyboard.press('Enter');
  assert.equal(await page.locator('details').getAttribute('open'), '');
  await page.keyboard.press('Tab');
  assert.equal(await page.locator(':focus').getAttribute('id'), 'refresh');
  assert.equal(await page.locator(':focus').evaluate(node => getComputedStyle(node).outlineWidth), '3px');
  await page.keyboard.press('Enter');
  assert.equal(await page.locator(':focus').getAttribute('id'), 'refresh');
  for (const [name, width, fontSize] of [['wide', 1200, '100%'], ['narrow', 320, '100%'], ['text-200', 1200, '200%']]) {
    await page.setViewportSize({ width, height: 1000 });
    await page.evaluate(value => { document.documentElement.style.fontSize = value; }, fontSize);
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
    if (process.env.SHELL_SCREENSHOT_DIR) {
      await mkdir(process.env.SHELL_SCREENSHOT_DIR, { recursive: true });
      await page.screenshot({ path: path.join(process.env.SHELL_SCREENSHOT_DIR, `shell-browser-${name}.png`), fullPage: true });
    }
  }
  await page.emulateMedia({ reducedMotion: 'reduce', forcedColors: 'active' });
  assert.equal(await page.locator('#refresh').evaluate(node => getComputedStyle(node).transitionDuration), '0s');
  assert.equal(await page.locator('.status').evaluate(node => getComputedStyle(node).borderStyle), 'solid');
  assert.deepEqual(errors, []);
  await page.close();
});

test('未配線IPC fixtureは接続成功にせず、HTML風不正応答も解釈しない', async () => {
  for (const valid of [true, false]) {
    const page = await browser.newPage();
    await page.addInitScript(valid => {
      window.__TAURI__ = { core: { invoke: async () => ({ protocolVersion: 1,
        integration: valid ? 'not_connected' : '<img src=x onerror="window.injected=true">',
        canStart: false, canStop: false, termination: 'unconfirmed' }) } };
    }, valid);
    await page.goto(url);
    await page.waitForFunction(() => document.querySelector('#status-title').textContent !== '状態を確認中');
    assert.equal(await page.locator('#status-title').innerText(), valid ? 'バックエンド未接続' : '状態を取得できません');
    assert.equal(await page.getByRole('button', { name: 'サポートを開始' }).isDisabled(), true);
    assert.equal(await page.locator('img').count(), 0);
    assert.equal(await page.evaluate(() => window.injected), undefined);
    await page.close();
  }
});

test('Rust待機中にも診断を開き、browser画面を破棄できる', async () => {
  const page = await browser.newPage();
  await page.addInitScript(() => { window.__TAURI__ = { core: { invoke: () => new Promise(() => {}) } }; });
  await page.goto(url);
  assert.equal(await page.locator('#status-title').innerText(), '状態を確認中');
  await page.getByText('診断情報', { exact: true }).click();
  assert.equal(await page.locator('details').getAttribute('open'), '');
  await page.close();
  assert.equal(page.isClosed(), true);
});
