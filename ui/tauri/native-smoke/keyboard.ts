import assert from 'node:assert/strict';
export type UiKey = 'Tab' | 'Shift+Tab' | 'Enter' | 'Space';
interface Boundary { press(key: UiKey): Promise<void>; read(): Promise<unknown>; settle(): Promise<void> }
export async function checkKeyboard(boundary: Boundary): Promise<{ cycles: number; checks: number }> {
  let checks = 0;
  const expect = async (focused: string, expanded: boolean, keyboard = true) => {
    const value = await boundary.read();
    assert.ok(value !== null && typeof value === 'object' && !Array.isArray(value));
    const view = value as Record<string, unknown>;
    assert.deepEqual({ focused: view.focused, expanded: view.expanded, title: view.title, startDisabled: view.startDisabled, stopDisabled: view.stopDisabled, diagnostic: view.diagnostic, busy: view.busy },
      { focused, expanded, title: 'バックエンド未接続', startDisabled: true, stopDisabled: true, diagnostic: expanded ? 'SHELL_UNWIRED' : null, busy: false });
    if (keyboard) {
      assert.equal(view.focusVisible, true);
      assert.ok(typeof view.outlineWidth === 'number' && view.outlineWidth >= 1);
    }
    checks += 1;
  };
  await expect('diagnostics', true, false);
  for (const activation of ['Enter', 'Space', 'Enter'] as const) {
    await boundary.press('Tab'); await expect('refresh', true);
    await boundary.press('Shift+Tab'); await expect('diagnostics', true);
    await boundary.press('Space'); await expect('diagnostics', false);
    await boundary.press('Enter'); await expect('diagnostics', true);
    await boundary.press('Tab'); await expect('refresh', true);
    await boundary.press(activation); await boundary.settle(); await expect('refresh', true);
    await boundary.press('Shift+Tab'); await expect('diagnostics', true);
  }
  return { cycles: 3, checks };
}
