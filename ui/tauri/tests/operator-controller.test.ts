import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createOperatorController } from '../src/operator/controller.ts';
import type { OperatorAdapter, OperatorState } from '../src/operator/model.ts';

function setup() {
  let state: OperatorState | undefined;
  const adapter: OperatorAdapter = {
    mode: 'mock', requestApproval: () => Promise.resolve(), requestStop: () => Promise.resolve(),
    subscribe: () => () => {}
  };
  const controller = createOperatorController({ adapter, onChange: value => { state = value; } });
  return { controller, state: () => state };
}

void test('サンプルの受取りだけでは依頼や接続を始めず、確認画面を表示する', () => {
  const { controller, state } = setup();
  controller.acceptSample('code');
  assert.equal(state()?.phase, 'review');
  assert.equal(state()?.sample, 'code');
  assert.equal(state()?.termination, 'not_started');
});
