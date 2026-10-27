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
