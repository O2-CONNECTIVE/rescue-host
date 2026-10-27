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
