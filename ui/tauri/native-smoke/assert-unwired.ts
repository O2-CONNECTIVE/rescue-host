import assert from 'node:assert/strict';
export interface NativeView { title: string; startDisabled: boolean; stopDisabled: boolean; permissionValues: string[]; diagnostic: string | null; diagnosticExpanded: boolean }
interface Boundary { read(): Promise<unknown>; openDiagnostics(): Promise<void>; invoke(): Promise<unknown> }
export async function checkUnwired(boundary: Boundary): Promise<{ checks: number }> {
  const expected = { title: 'バックエンド未接続', startDisabled: true, stopDisabled: true, permissionValues: ['未確認', '未確認', '未確認', '未確認'] };
  assert.deepEqual(await boundary.read(), { ...expected, diagnostic: null, diagnosticExpanded: false });
  assert.deepEqual(await boundary.invoke(), { protocolVersion: 1, integration: 'not_connected', canStart: false, canStop: false, termination: 'unconfirmed' });
  await boundary.openDiagnostics();
  assert.deepEqual(await boundary.read(), { ...expected, diagnostic: 'SHELL_UNWIRED', diagnosticExpanded: true });
  return { checks: 3 };
}
