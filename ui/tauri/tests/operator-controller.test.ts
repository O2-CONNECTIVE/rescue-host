import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createOperatorController } from '../src/operator/controller.ts';
import type { OperatorAdapter, OperatorState } from '../src/operator/model.ts';

function setup() {
  let state: OperatorState | undefined;
  let listener = (_event: unknown) => {};
  let flow = 0;
  const requests: string[] = [];
  const adapter: OperatorAdapter = {
    mode: 'mock', requestApproval: value => { flow = value; requests.push('approval'); return Promise.resolve(); },
    requestStop: () => { requests.push('stop'); return Promise.resolve(); },
    subscribe: value => { listener = value; return () => { listener = () => {}; }; }
  };
  const controller = createOperatorController({ adapter, onChange: value => { state = value; } });
  return { controller, state: () => state, requests, emit: (event: string) => { listener({ flow, event }); } };
}

void test('サンプルの受取りだけでは依頼や接続を始めず、確認画面を表示する', () => {
  const { controller, state } = setup();
  controller.acceptSample('code');
  assert.equal(state()?.phase, 'review');
  assert.equal(state()?.sample, 'code');
  assert.equal(state()?.termination, 'not_started');
});

void test('確認後の依頼受付・承認・接続完了をそれぞれ別の状態として扱う', async () => {
  const { controller, requests, emit } = setup();
  controller.acceptSample('definition');
  await controller.requestApproval(false);
  assert.equal(controller.snapshot().phase, 'review');
  assert.deepEqual(requests, []);
  await controller.requestApproval(true);
  assert.equal(controller.snapshot().phase, 'waiting');
  assert.equal(controller.snapshot().termination, 'unconfirmed');
  assert.deepEqual(requests, ['approval']);
  emit('approved');
  assert.equal(controller.snapshot().phase, 'connecting');
  emit('connected');
  assert.equal(controller.snapshot().phase, 'connected');
  assert.equal(controller.snapshot().termination, 'unconfirmed');
});
