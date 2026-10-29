import { writeFile } from 'node:fs/promises';
import path from 'node:path';
import { Driver } from './webdriver.ts';
import { createNativeObservation } from './observation.ts';
import { observeSessionStartup } from './session-observation.ts';
import { inspectBaselineDocument } from './baseline-document.ts';
import { captureNavigationSnapshot, classifyNavigationSnapshots } from './baseline-navigation.ts';

// UI/製品IPC判定へ入らない独立対照。終了操作はsupervisorだけが行う。
const output = process.env.NATIVE_OUTPUT_DIR;
let observation: ReturnType<typeof createNativeObservation> | undefined;
let deadline: ReturnType<typeof setTimeout> | undefined;
const session = { schema: 1, kind: 'direct-wry-controller-contrast', productNativePass: false,
  sessionCreated: false, documentChecked: false, browserVersion: null as string | null, sessionOutcome: 'not-started', failurePhase: 'session-create' };
try {
  const application = process.env.NATIVE_APP_EXE; const profile = process.env.NATIVE_PROFILE;
  const logs = process.env.NATIVE_TOOLS_DIR;
  if (process.platform !== 'win32' || !application || !profile || !logs || !output) throw new Error('NATIVE_ENV_NOT_CONFIGURED');
  observation = createNativeObservation(output);
  deadline = setTimeout(() => {
    try { observation?.fail(new Error('NATIVE_TEST_DEADLINE')); } catch { /* 固定失敗だけを出す。 */ }
    process.exit(1);
  }, 35000);
  const driver = new Driver('http://127.0.0.1:4444', fetch, value => { observation?.request(value); });
  observation.phase('session-create');
  try {
    session.browserVersion = await observeSessionStartup(() => driver.create(application, profile), logs, output);
    session.sessionCreated = true; session.sessionOutcome = 'ok';
  } catch (error) {
    session.sessionOutcome = error instanceof Error && error.message === 'NATIVE_DRIVER_TIMEOUT' ? 'timeout' : 'request-failed';
    throw error;
  }
  session.failurePhase = 'document';
  observation.phase('origin-check');
  let document: unknown;
  const navigationDir = path.join(logs, 'baseline-trace');
  const navigationBefore = await captureNavigationSnapshot(navigationDir, 'before');
  try {
    document = await driver.execute("const marker = document.querySelector('#wry-creation-contrast'); return {href:location.href,uri:document.documentURI,origin:location.origin,marker:marker?.textContent??null,markerPresent:marker!==null,tauri:typeof window.__TAURI__,readyState:document.readyState};");
  } catch (error) {
    // 応答自体がない場合は原エラーを保ち、各項目をunknownにする。
    try { await writeFile(path.join(output, 'baseline-document-report.json'), JSON.stringify({ schema: 1, purpose: 'diagnostic-only', ...inspectBaselineDocument(null) })); }
    catch { /* 診断欠測は元の失敗を置き換えない。 */ }
    throw error;
  } finally {
    const navigationAfter = await captureNavigationSnapshot(navigationDir, 'after');
    try { await writeFile(path.join(output, 'baseline-navigation-report.json'), JSON.stringify({ schema: 1, purpose: 'diagnostic-only', ...classifyNavigationSnapshots(navigationBefore, navigationAfter) })); }
    catch { /* 診断欠測は元の判定を置き換えない。 */ }
  }
  const documentReport = inspectBaselineDocument(document);
  await writeFile(path.join(output, 'baseline-document-report.json'), JSON.stringify({ schema: 1, purpose: 'diagnostic-only', ...documentReport }));
  if (!documentReport.accepted) throw new Error('NATIVE_BASELINE_DOCUMENT_REJECTED');
  session.documentChecked = true; session.failurePhase = 'complete';
  observation.pass();
} catch (error) {
  try { observation?.fail(error); } catch { /* 原エラーを出力しない。 */ }
  process.exitCode = 1;
} finally {
  clearTimeout(deadline);
  if (output) {
    try { await writeFile(path.join(output, 'baseline-session.json'), JSON.stringify(session)); }
    catch { process.exitCode = 1; }
  }
}
