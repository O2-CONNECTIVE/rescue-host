import { after } from 'node:test';
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

export async function page() {
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
export async function receive(instance: Page) {
  await instance.getByRole('button', { name: 'サンプルを入力', exact: true }).click();
  await instance.getByRole('button', { name: '接続内容を確認', exact: true }).click();
  await instance.getByRole('checkbox').check();
}
export async function details(instance: Page) {
  const trigger = instance.getByRole('button', { name: '詳細を見る', exact: true });
  if (await trigger.getAttribute('aria-expanded') !== 'true') await trigger.click();
}
export async function respond(instance: Page, name: string) {
  await details(instance); await instance.getByRole('button', { name, exact: true }).click();
}
export async function prepared(instance: Page) {
  await receive(instance);
  await instance.getByRole('button', { name: '承認を依頼（サンプル）', exact: true }).click();
  await respond(instance, '承認応答を再現');
  await respond(instance, '接続情報の準備完了を再現');
}
export async function screenshot(instance: Page, name: string) {
  if (!process.env.OPERATOR_SCREENSHOT_DIR) return;
  await mkdir(process.env.OPERATOR_SCREENSHOT_DIR, { recursive: true });
  await instance.screenshot({ path: path.join(process.env.OPERATOR_SCREENSHOT_DIR, name), fullPage: true });
}
