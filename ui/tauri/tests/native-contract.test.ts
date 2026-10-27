import { test } from 'node:test';
import assert from 'node:assert/strict';
import { checkUnwired, type NativeView } from '../native-smoke/assert-unwired.ts';

const view = (): NativeView => ({ title: 'バックエンド未接続', startDisabled: true, stopDisabled: true, permissionValues: ['未確認', '未確認', '未確認', '未確認'], diagnostic: null, diagnosticExpanded: false });
const payload = () => ({ protocolVersion: 1, integration: 'not_connected', canStart: false, canStop: false, termination: 'unconfirmed' });
void test('native境界はUIと実IPC両方の未配線条件を要求する', async () => {
  let opened = false;
  const result = await checkUnwired({ read: () => Promise.resolve({ ...view(), diagnosticExpanded: opened, diagnostic: opened ? 'SHELL_UNWIRED' : null }), openDiagnostics: () => { opened = true; return Promise.resolve(); }, invoke: () => Promise.resolve(payload()) });
  assert.equal(result.checks, 3);
  assert.equal(opened, true);
});
void test('成功風IPCと開始可能なUIを拒否し、0件成功へ変換しない', async () => {
  const cases: [NativeView, unknown][] = [[view(), { ...payload(), canStart: true }], [{ ...view(), startDisabled: false }, payload()]];
  for (const [current, status] of cases) {
    await assert.rejects(checkUnwired({ read: () => Promise.resolve(current), openDiagnostics: () => Promise.resolve(), invoke: () => Promise.resolve(status) }));
  }
});

void test('native keyboard境界は逆順focusと反復開閉を3周し、各到達先を確認する', async () => {
  const { checkKeyboard } = await import('../native-smoke/keyboard.ts');
  const keys: string[] = [];
  const sample = (focused: string, expanded: boolean) => ({ focused, expanded, title: 'バックエンド未接続', startDisabled: true, stopDisabled: true, diagnostic: expanded ? 'SHELL_UNWIRED' : null, busy: false, focusVisible: true, outlineWidth: 3 });
  const cycle = [sample('refresh', true), sample('diagnostics', true), sample('diagnostics', false), sample('diagnostics', true), sample('refresh', true), sample('refresh', true), sample('diagnostics', true)];
  const snapshots = [sample('diagnostics', true), ...cycle, ...cycle, ...cycle];
  const result = await checkKeyboard({ press: key => { keys.push(key); return Promise.resolve(); }, read: () => Promise.resolve(snapshots.shift()), armRefresh: () => Promise.resolve(), disarmRefresh: () => Promise.resolve(), settle: () => Promise.resolve({ started: true, completed: true }) });
  assert.deepEqual(result, { cycles: 3, checks: 22, refreshes: 3 });
  assert.equal(snapshots.length, 0);
  assert.deepEqual(keys, [
    'Tab', 'Shift+Tab', 'Space', 'Enter', 'Tab', 'Enter', 'Shift+Tab',
    'Tab', 'Shift+Tab', 'Space', 'Enter', 'Tab', 'Space', 'Shift+Tab',
    'Tab', 'Shift+Tab', 'Space', 'Enter', 'Tab', 'Enter', 'Shift+Tab'
  ]);
});

void test('native keyboard境界はfocus逸脱・outline消失・無効操作の有効化を拒否する', async () => {
  const { checkKeyboard } = await import('../native-smoke/keyboard.ts');
  const initial = { focused: 'diagnostics', expanded: true, title: 'バックエンド未接続', startDisabled: true, stopDisabled: true, diagnostic: 'SHELL_UNWIRED', busy: false, focusVisible: false, outlineWidth: 0 };
  for (const bad of [{ focused: 'other' }, { focusVisible: false }, { outlineWidth: 0 }, { startDisabled: false }]) {
    const snapshots = [initial, { ...initial, focused: 'refresh', focusVisible: true, outlineWidth: 3, ...bad }];
    let presses = 0;
    await assert.rejects(checkKeyboard({ press: () => { presses += 1; return Promise.resolve(); }, read: () => Promise.resolve(snapshots.shift()), armRefresh: () => Promise.resolve(), disarmRefresh: () => Promise.resolve(), settle: () => Promise.resolve({ started: true, completed: true }) }));
    assert.equal(presses, 1);
  }
});

void test('再確認キーが無反応なら旧状態の維持をnative再確認の成功にしない', async () => {
  const { checkKeyboard } = await import('../native-smoke/keyboard.ts');
  let focused = 'diagnostics';
  let expanded = true;
  await assert.rejects(checkKeyboard({
    press: key => {
      if (key === 'Tab') focused = 'refresh';
      else if (key === 'Shift+Tab') focused = 'diagnostics';
      else if (focused === 'diagnostics') expanded = !expanded;
      return Promise.resolve();
    },
    read: () => Promise.resolve({ focused, expanded, title: 'バックエンド未接続', startDisabled: true, stopDisabled: true, diagnostic: expanded ? 'SHELL_UNWIRED' : null, busy: false, focusVisible: true, outlineWidth: 3 }),
    armRefresh: () => Promise.resolve(), disarmRefresh: () => Promise.resolve(),
    settle: () => Promise.resolve({ started: false, completed: false })
  }), /NATIVE_REFRESH_NOT_OBSERVED/);
});

void test('nativeキー送信失敗でもその回のrefresh観測を破棄する', async () => {
  const { checkKeyboard } = await import('../native-smoke/keyboard.ts');
  let focused = 'diagnostics';
  let expanded = true;
  let armed = false;
  await assert.rejects(checkKeyboard({
    press: key => {
      if (focused === 'refresh' && (key === 'Enter' || key === 'Space')) return Promise.reject(new Error('fixture-key-failure'));
      if (key === 'Tab') focused = 'refresh';
      else if (key === 'Shift+Tab') focused = 'diagnostics';
      else expanded = !expanded;
      return Promise.resolve();
    },
    read: () => Promise.resolve({ focused, expanded, title: 'バックエンド未接続', startDisabled: true, stopDisabled: true, diagnostic: expanded ? 'SHELL_UNWIRED' : null, busy: false, focusVisible: true, outlineWidth: 3 }),
    armRefresh: () => { armed = true; return Promise.resolve(); },
    settle: () => Promise.resolve({ started: true, completed: true }),
    disarmRefresh: () => { armed = false; return Promise.resolve(); }
  }), /fixture-key-failure/);
  assert.equal(armed, false);
});

void test('native失敗のphaseとrequest区分を残し、生の例外文やpayloadを公開しない', async t => {
  const { mkdtemp, readFile, rm } = await import('node:fs/promises');
  const { tmpdir } = await import('node:os');
  const path = await import('node:path');
  const { createNativeObservation } = await import('../native-smoke/observation.ts');
  const directory = await mkdtemp(path.join(tmpdir(), 'native-evidence-contract-'));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const evidence = createNativeObservation(directory);
  evidence.phase('session-create');
  evidence.request({ operation: 'create-session', outcome: 'timeout', elapsedMs: 15000 });
  evidence.fail(new Error('PRIVATE_FIXTURE_DETAIL /not-public/path'));
  const raw = await readFile(path.join(directory, 'native-observation.json'), 'utf8');
  const record: unknown = JSON.parse(raw);
  assert.ok(record !== null && typeof record === 'object' && 'phase' in record && 'status' in record && 'lastRequest' in record);
  assert.equal(record.phase, 'session-create');
  assert.equal(record.status, 'failed');
  assert.deepEqual(record.lastRequest, { operation: 'create-session', outcome: 'timeout', elapsedMs: 15000 });
  assert.equal(raw.includes('PRIVATE_FIXTURE_DETAIL'), false);
  assert.equal(raw.includes('/not-public/path'), false);
});

void test('WebDriver timeoutとHTTP拒否を識別し、生のdriverメッセージを捨てる', async t => {
  t.mock.method(globalThis, 'fetch', () => Promise.reject(new DOMException('PRIVATE_TIMEOUT_DETAIL', 'TimeoutError')));
  const { Driver } = await import('../native-smoke/webdriver.ts');
  const traces: { operation: string; outcome: string; elapsedMs: number; status?: number; driverError?: string }[] = [];
  const timeout = new Driver('http://127.0.0.1:4444', () => Promise.reject(new DOMException('PRIVATE_TIMEOUT_DETAIL', 'TimeoutError')), value => { traces.push(value); });
  await assert.rejects(timeout.request('POST', '/session', {}), /NATIVE_DRIVER_TIMEOUT/);
  assert.equal(traces.at(-1)?.operation, 'create-session');
  assert.equal(traces.at(-1)?.outcome, 'timeout');
  const rejected = new Driver('http://127.0.0.1:4444', () => Promise.resolve(new Response(JSON.stringify({ value: { error: 'session not created', message: 'PRIVATE_DRIVER_DETAIL /not-public/path' } }), { status: 500 })), value => { traces.push(value); });
  await assert.rejects(rejected.request('POST', '/session', {}), /NATIVE_DRIVER_REQUEST_FAILED/);
  assert.equal(traces.at(-1)?.outcome, 'http');
  assert.equal(traces.at(-1)?.driverError, 'session not created');
  assert.equal(JSON.stringify(traces).includes('PRIVATE_'), false);
  assert.equal(JSON.stringify(traces).includes('/not-public/path'), false);
});

void test('WebView観測の成功を所有プロセス終了の証明へ変換しない', async t => {
  const { mkdtemp, readFile, rm } = await import('node:fs/promises');
  const { tmpdir } = await import('node:os');
  const path = await import('node:path');
  const { createNativeObservation } = await import('../native-smoke/observation.ts');
  const directory = await mkdtemp(path.join(tmpdir(), 'native-stage-contract-'));
  t.after(() => rm(directory, { recursive: true, force: true }));
  createNativeObservation(directory).pass();
  const record: unknown = JSON.parse(await readFile(path.join(directory, 'native-observation.json'), 'utf8'));
  assert.ok(record !== null && typeof record === 'object' && 'status' in record);
  assert.equal(record.status, 'webview-passed-awaiting-close');
  assert.equal('normalCloseVerified' in record, false);
});
