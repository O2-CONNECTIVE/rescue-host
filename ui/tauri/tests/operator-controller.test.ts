import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createOperatorController } from '../src/operator/controller.ts';
import type { OperatorAdapter, OperatorState } from '../src/operator/model.ts';
import { createMemoryHistory } from '../src/operator/history.ts';

function setup(overrides: Partial<OperatorAdapter> = {}, history?: ReturnType<typeof createMemoryHistory>) {
  let state: OperatorState | undefined;
  let listener: (event: unknown) => void = () => {};
  let flow = 0;
  const requests: string[] = [];
  const adapter: OperatorAdapter = {
    mode: 'mock', requestApproval: value => { flow = value; requests.push('approval'); return Promise.resolve(); },
    requestStop: () => { requests.push('stop'); return Promise.resolve(); },
    queryStatus: () => { requests.push('status'); return Promise.resolve(); },
    subscribe: value => { listener = value; return () => { listener = () => {}; }; }, ...overrides
  };
  const controller = createOperatorController({ adapter, history, onChange: value => { state = value; } });
  return { controller, state: () => state, requests, raw: (value: unknown) => { listener(value); },
    emit: (event: string) => { listener({ flow, event }); } };
}

void test('サンプルの受取りだけでは依頼や接続を始めず、確認画面を表示する', () => {
  const { controller, state } = setup();
  controller.acceptSample('code');
  assert.equal(state()?.phase, 'review');
  assert.equal(state()?.sample, 'code');
  assert.equal(state()?.termination, 'not_started');
});

void test('履歴削除は終了未確認を解除せず、遅い終了応答で削除済み履歴を復活させない', async () => {
  const history = createMemoryHistory({ now: () => Date.UTC(2026, 9, 28) });
  const { controller, emit, requests } = setup({}, history);
  controller.acceptSample('code'); await controller.requestApproval(true);
  emit('approved'); emit('prepared'); emit('connected');
  await controller.requestStop(); emit('lost');
  assert.equal(history.entries().length, 1);
  assert.deepEqual(Object.keys(history.entries()[0] ?? {}).sort(), ['displayName', 'occurredAt', 'result']);
  history.clear();
  controller.reset(); controller.acceptSample('definition'); await controller.requestApproval(true);
  assert.equal(controller.snapshot().phase, 'unconfirmed');
  assert.deepEqual(requests, ['approval', 'stop']);
  emit('stop_confirmed');
  assert.equal(controller.snapshot().termination, 'confirmed');
  assert.equal(history.entries().length, 0);
});

void test('初期化した体験へ古い観測や遅延した要求失敗を適用しない', async () => {
  let rejectStop: (reason: Error) => void = () => {};
  const stop = new Promise<void>((_resolve, reject) => { rejectStop = reject; });
  const { controller, emit, raw } = setup({ requestStop: () => stop });
  controller.acceptSample('code'); await controller.requestApproval(true);
  emit('approved'); emit('prepared'); emit('connected');
  const pending = controller.requestStop();
  emit('stop_confirmed');
  controller.reset();
  assert.equal(controller.snapshot().phase, 'entry');
  controller.acceptSample('definition');
  raw({ flow: 1, event: 'stop_confirmed' });
  rejectStop(new Error('private fixture detail must not be shown'));
  await pending;
  assert.equal(controller.snapshot().phase, 'review');
  assert.equal(controller.snapshot().diagnostic, 'DEMO_REVIEW');
  controller.dispose();
  const before = controller.snapshot();
  raw({ flow: 2, event: 'connected' });
  controller.reset(); controller.acceptSample('code');
  assert.equal(controller.snapshot(), before);
});

void test('未知の観測は生文字列を出さず閉じ、別の体験の応答は無視する', async () => {
  const { controller, raw } = setup();
  controller.acceptSample('code'); await controller.requestApproval(true);
  raw({ flow: 99, event: 'connected' });
  assert.equal(controller.snapshot().phase, 'waiting');
  raw({ flow: 1, event: '<img src=x onerror=alert(1)>' });
  assert.equal(controller.snapshot().phase, 'unconfirmed');
  assert.equal(controller.snapshot().diagnostic, 'DEMO_INVALID_OBSERVATION');
  assert.equal(controller.snapshot().termination, 'unconfirmed');
});

void test('要求の失敗は固定表示にし、接続や終了の成功へ読み替えない', async () => {
  for (const operation of ['approval', 'stop'] as const) {
    const { controller, emit } = setup(operation === 'approval'
      ? { requestApproval: () => Promise.reject(new Error('private approval detail')) }
      : { requestStop: () => Promise.reject(new Error('private stop detail')) });
    controller.acceptSample('code');
    await controller.requestApproval(true);
    if (operation === 'stop') {
      emit('approved'); emit('prepared'); emit('connected'); await controller.requestStop();
    }
    assert.equal(controller.snapshot().phase, operation === 'approval' ? 'failed' : 'unconfirmed');
    assert.equal(controller.snapshot().termination, 'unconfirmed');
    assert.match(controller.snapshot().diagnostic, /^DEMO_(REQUEST_FAILED|TERMINATION_UNCONFIRMED)$/);
  }
});

void test('期限切れ・拒否・失敗・停止未確認は接続完了応答で終了確認に変わらない', async () => {
  for (const [advance, event, expected] of [
    [false, 'expired', 'expired'], [false, 'denied', 'failed'],
    [false, 'failed', 'failed'], [true, 'lost', 'unconfirmed']
  ] as const) {
    const { controller, emit } = setup();
    controller.acceptSample('code'); await controller.requestApproval(true);
    if (advance) { emit('approved'); emit('prepared'); emit('connected'); await controller.requestStop(); }
    emit(event);
    assert.equal(controller.snapshot().phase, expected);
    assert.equal(controller.snapshot().termination, event === 'denied' ? 'not_started' : 'unconfirmed');
    emit('connected');
    assert.equal(controller.snapshot().phase, expected);
    assert.equal(controller.snapshot().termination, event === 'denied' ? 'not_started' : 'unconfirmed');
  }
});

void test('停止要求の受付と繰返し操作だけでは終了確認にせず、別の応答を待つ', async () => {
  const { controller, requests, emit } = setup();
  controller.acceptSample('code');
  await controller.requestApproval(true);
  emit('approved'); emit('prepared'); emit('connected');
  await controller.requestStop();
  assert.equal(controller.snapshot().phase, 'stopping');
  assert.equal(controller.snapshot().termination, 'unconfirmed');
  await controller.requestStop();
  assert.deepEqual(requests, ['approval', 'stop']);
  emit('stop_confirmed');
  assert.equal(controller.snapshot().phase, 'stopped');
  assert.equal(controller.snapshot().termination, 'confirmed');
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
  emit('prepared');
  assert.equal(controller.snapshot().phase, 'ready');
  emit('connected');
  assert.equal(controller.snapshot().phase, 'connected');
  assert.equal(controller.snapshot().termination, 'unconfirmed');
});

void test('トレイ格納は接続を維持し、停止未確認では次へ進まず有効な同一接続の状態だけ再確認する', async () => {
  const { controller, emit, requests } = setup();
  controller.acceptSample('code'); await controller.requestApproval(true);
  emit('approved'); emit('prepared'); emit('connected');
  controller.minimizeToTray();
  assert.equal(controller.snapshot().visibility, 'tray');
  assert.equal(controller.snapshot().phase, 'connected');
  assert.equal(controller.snapshot().termination, 'unconfirmed');
  controller.restoreWindow();
  assert.equal(controller.snapshot().visibility, 'window');
  controller.reset(); controller.acceptSample('definition');
  assert.equal(controller.snapshot().phase, 'connected');
  await controller.requestStop(); emit('lost');
  controller.reset(); controller.acceptSample('definition');
  await controller.requestApproval(true);
  assert.equal(controller.snapshot().phase, 'unconfirmed');
  await controller.retryStatus();
  assert.deepEqual(requests, ['approval', 'stop', 'status']);
  assert.equal(controller.snapshot().checkingStatus, true);
  emit('status_active');
  assert.equal(controller.snapshot().phase, 'unconfirmed');
  assert.equal(controller.snapshot().stopRequested, true);
  emit('expired');
  await controller.retryStatus(); emit('approved'); emit('prepared'); emit('status_active');
  assert.equal(controller.snapshot().phase, 'expired');
  assert.deepEqual(requests, ['approval', 'stop', 'status']);
  emit('stop_confirmed');
  assert.equal(controller.snapshot().termination, 'confirmed');
  controller.reset();
  assert.equal(controller.snapshot().phase, 'entry');
});
