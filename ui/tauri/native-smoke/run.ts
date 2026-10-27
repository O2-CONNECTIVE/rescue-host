import { writeFile } from 'node:fs/promises';
import path from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';
import { Driver } from './webdriver.ts';
import { checkUnwired } from './assert-unwired.ts';

const output = process.env.NATIVE_OUTPUT_DIR;
const application = process.env.NATIVE_APP_EXE;
const profile = process.env.NATIVE_PROFILE;
if (process.platform !== 'win32' || !output || !application || !profile) throw new Error('NATIVE_ENV_NOT_CONFIGURED');
const deadline = setTimeout(() => { console.error('NATIVE_TEST_DEADLINE'); process.exit(1); }, 60000);
try {
  const driver = new Driver('http://127.0.0.1:4444');
  const browserVersion = await driver.create(application, profile);
  const origin = await driver.execute('return location.origin;');
  if (origin !== 'http://tauri.localhost') throw new Error('NATIVE_ORIGIN_MISMATCH');
  const until = Date.now() + 10000;
  let title: unknown;
  do {
    title = await driver.execute("return document.querySelector('#status-title')?.textContent ?? null;");
    if (title && title !== '状態を確認中') break;
    await delay(100);
  } while (Date.now() < until);
  await writeFile(path.join(output, 'native-main.png'), await driver.screenshot());
  const result = await checkUnwired({
    read: () => driver.execute(`return {
      title: document.querySelector('#status-title')?.textContent ?? '',
      startDisabled: [...document.querySelectorAll('button')].find(b=>b.textContent==='サポートを開始')?.disabled === true,
      stopDisabled: [...document.querySelectorAll('button')].find(b=>b.textContent==='接続を停止')?.disabled === true,
      permissionValues: [...document.querySelectorAll('.shell-permissions dd')].map(n=>n.textContent),
      diagnostic: document.querySelector('#diagnostic')?.textContent ?? null,
      diagnosticExpanded: document.querySelector('[data-slot="collapsible-trigger"]')?.getAttribute('aria-expanded') === 'true'
    };`),
    openDiagnostics: () => driver.click('[data-slot="collapsible-trigger"]'),
    invoke: () => driver.invoke()
  });
  if (result.checks !== 3) throw new Error('NATIVE_ZERO_OR_INCOMPLETE_CHECKS');
  await driver.execute("document.querySelector('[data-slot=\"collapsible-content\"]')?.scrollIntoView({block:'end',behavior:'instant'}); return true;");
  await writeFile(path.join(output, 'native-diagnostics.png'), await driver.screenshot());
  await writeFile(path.join(output, 'webview-result.json'), JSON.stringify({ phase: 'webview-observed-awaiting-normal-close', nativeTestsStarted: 1, checks: result.checks, browserVersion, actualRustIpc: true }));
  // Keep both drivers alive. The supervisor must observe normal close before any session/driver cleanup.
  console.log('NATIVE_WEBVIEW_AND_REAL_IPC_OBSERVED');
} catch {
  console.error('NATIVE_WEBVIEW_OR_IPC_FAILED');
  process.exitCode = 1;
} finally { clearTimeout(deadline); }
