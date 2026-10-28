import { test } from "node:test";
import assert from "node:assert/strict";
import { readFile } from 'node:fs/promises';
import { createShellController, type ShellState } from "../src/controller.ts";

const unwired = () => ({ protocolVersion: 1, integration: "not_connected", canStart: false, canStop: false, termination: "unconfirmed" });

void test('共有shell v1 fixtureをUIの公開invoke境界で受理・拒否する', async () => {
  const fixture: unknown = JSON.parse(await readFile(new URL('../contracts/shell-status-v1.json', import.meta.url), 'utf8'));
  const record = (value: unknown): value is Record<string, unknown> => value !== null && typeof value === 'object' && !Array.isArray(value);
  assert.ok(record(fixture));
  assert.equal(fixture.scope, 'unwired-shell-status-v1');
  assert.ok('response' in fixture);
  const observe = async (payload: unknown) => {
    const states: ShellState[] = [];
    const controller = createShellController({ invoke: (command, args) => {
      assert.equal(command, fixture.command);
      assert.deepEqual(args, { request: fixture.request });
      return Promise.resolve(payload);
    }, onChange: state => states.push(state) });
    await controller.refresh();
    controller.dispose();
    return states;
  };
  assert.deepEqual(await observe(fixture.response), [{ kind: 'checking', diagnostic: 'CHECKING' }, { kind: 'not_connected', diagnostic: 'SHELL_UNWIRED' }]);
  assert.ok(Array.isArray(fixture.rejectedResponses));
  const responses: unknown[] = fixture.rejectedResponses;
  assert.equal(responses.length, 12);
  const ids = new Set<string>();
  for (const sample of responses) {
    assert.ok(record(sample) && typeof sample.id === 'string' && sample.id.length > 0 && 'payload' in sample);
    assert.ok(!ids.has(sample.id)); ids.add(sample.id);
    assert.deepEqual(await observe(sample.payload), [{ kind: 'checking', diagnostic: 'CHECKING' }, { kind: 'unavailable', diagnostic: 'INVALID_RESPONSE' }], sample.id);
  }
});

void test("Rustの未配線応答を受けても開始と停止は不可、終了状態は未確認", async () => {
  const states: ShellState[] = [];
  const controller = createShellController({ invoke: (command, args) => {
    assert.equal(command, "get_shell_status");
    assert.deepEqual(args, { request: { protocolVersion: 1 } });
    return Promise.resolve(unwired());
  }, onChange: state => states.push(state) });
  await controller.refresh();
  assert.deepEqual(states.at(-1), { kind: "not_connected", diagnostic: "SHELL_UNWIRED" });
});

void test("未対応の成功風payloadを未接続の確認として受理しない", async () => {
  for (const payload of [null, [], { ...unwired(), integration: "connected" },
    { ...unwired(), canStart: true }, { ...unwired(), canStop: true },
    { ...unwired(), termination: "confirmed" }, { ...unwired(), protocolVersion: 2 },
    { ...unwired(), unexpected: "field" }]) {
    const states: ShellState[] = [];
    const controller = createShellController({ invoke: () => Promise.resolve(payload), onChange: state => states.push(state) });
    await controller.refresh();
    assert.deepEqual(states.at(-1), { kind: "unavailable", diagnostic: "INVALID_RESPONSE" });
  }
});

void test("IPC失敗を秘密を含まない取得不能として表示する", async () => {
  const states: ShellState[] = [];
  const controller = createShellController({ invoke: () => Promise.reject(new Error("untrusted-error-detail")), onChange: state => states.push(state) });
  await controller.refresh();
  assert.deepEqual(states.at(-1), { kind: "unavailable", diagnostic: "IPC_ERROR" });
});

void test("Rustが応答しなくても有限待機後に取得不能になる", async () => {
  const states: ShellState[] = [];
  const controller = createShellController({ invoke: () => new Promise(() => {}), onChange: state => states.push(state), timeoutMs: 5 });
  const result = await Promise.race([controller.refresh().then(() => "finished"), new Promise(resolve => setTimeout(() => resolve("unfinished"), 50))]);
  assert.equal(result, "finished");
  assert.deepEqual(states.at(-1), { kind: "unavailable", diagnostic: "IPC_TIMEOUT" });
});

void test("画面を破棄した後は遅延応答や再確認で表示を更新しない", async () => {
  let reply: (value: unknown) => void = () => { throw new Error("reply not initialized"); };
  const states: ShellState[] = [];
  const controller = createShellController({ invoke: () => new Promise(resolve => { reply = resolve; }), onChange: state => states.push(state) });
  const pending = controller.refresh();
  controller.dispose();
  await pending;
  reply(unwired());
  await controller.refresh();
  assert.deepEqual(states, [{ kind: "checking", diagnostic: "CHECKING" }]);
});

void test('待機中の再確認を重複せず、timeout後の遅延応答を反映しない', async () => {
  let calls = 0;
  let reply: (value: unknown) => void = () => { throw new Error('reply not initialized'); };
  const states: ShellState[] = [];
  const controller = createShellController({ invoke: () => {
    calls += 1;
    return new Promise(resolve => { reply = resolve; });
  }, onChange: state => states.push(state), timeoutMs: 5 });
  const first = controller.refresh();
  await controller.refresh();
  assert.equal(calls, 1);
  await first;
  reply(unwired());
  await Promise.resolve();
  assert.deepEqual(states, [{ kind: 'checking', diagnostic: 'CHECKING' }, { kind: 'unavailable', diagnostic: 'IPC_TIMEOUT' }]);
  controller.dispose();
});

void test('readerの固定faultだけを診断へ渡し、終了確認不能と取得不可を保つ', async () => {
  for (const code of ['VERSION_MISMATCH', 'INVALID_MESSAGE', 'CONTEXT_MISMATCH', 'TERMINATION_UNCONFIRMED']) {
    const states: ShellState[] = [];
    const controller = createShellController({
      // Native Tauri Err(&str) is a string rejection, not a JavaScript Error.
      // eslint-disable-next-line @typescript-eslint/prefer-promise-reject-errors
      invoke: () => Promise.reject(code),
      onChange: state => states.push(state)
    });
    await controller.refresh();
    assert.deepEqual(states.at(-1), { kind: 'unavailable', diagnostic: code });
    controller.dispose();
  }
});

void test('reader fault風の未知文字列やobjectを診断へ転送しない', async () => {
  for (const payload of ['unknown-detail', '<img src=x>', { code: 'INVALID_MESSAGE' }, new Error('INVALID_MESSAGE')]) {
    const states: ShellState[] = [];
    const controller = createShellController({
      // Exercise untrusted native rejection values; only exact fixed strings are allowed.
      // eslint-disable-next-line @typescript-eslint/prefer-promise-reject-errors
      invoke: () => Promise.reject(payload),
      onChange: state => states.push(state)
    });
    await controller.refresh();
    assert.deepEqual(states.at(-1), { kind: 'unavailable', diagnostic: 'IPC_ERROR' });
    controller.dispose();
  }
});
