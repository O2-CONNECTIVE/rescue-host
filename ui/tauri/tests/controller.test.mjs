import { test } from "node:test";
import assert from "node:assert/strict";
import { createShellController } from "../web/controller.mjs";

const unwired = () => ({ protocolVersion: 1, integration: "not_connected", canStart: false, canStop: false, termination: "unconfirmed" });

test("Rustの未配線応答を受けても開始と停止は不可、終了状態は未確認", async () => {
  const states = [];
  const controller = createShellController({ invoke: async () => unwired(), onChange: state => states.push(state) });
  await controller.refresh();
  assert.deepEqual(states.at(-1), { kind: "not_connected", diagnostic: "SHELL_UNWIRED" });
});

test("未対応の成功風payloadを未接続の確認として受理しない", async () => {
  const states = [];
  const controller = createShellController({ invoke: async () => ({ ...unwired(), integration: "connected", canStart: true }), onChange: state => states.push(state) });
  await controller.refresh();
  assert.deepEqual(states.at(-1), { kind: "unavailable", diagnostic: "INVALID_RESPONSE" });
});

test("IPC失敗を秘密を含まない取得不能として表示する", async () => {
  const states = [];
  const controller = createShellController({ invoke: async () => { throw new Error("untrusted-error-detail"); }, onChange: state => states.push(state) });
  await controller.refresh();
  assert.deepEqual(states.at(-1), { kind: "unavailable", diagnostic: "IPC_ERROR" });
});

test("Rustが応答しなくても有限待機後に取得不能になる", async () => {
  const states = [];
  const controller = createShellController({ invoke: () => new Promise(() => {}), onChange: state => states.push(state), timeoutMs: 5 });
  const result = await Promise.race([controller.refresh().then(() => "finished"), new Promise(resolve => setTimeout(() => resolve("unfinished"), 50))]);
  assert.equal(result, "finished");
  assert.deepEqual(states.at(-1), { kind: "unavailable", diagnostic: "IPC_TIMEOUT" });
});

test("画面を破棄した後は遅延応答や再確認で表示を更新しない", async () => {
  let reply;
  const states = [];
  const controller = createShellController({ invoke: () => new Promise(resolve => { reply = resolve; }), onChange: state => states.push(state) });
  const pending = controller.refresh();
  controller.dispose();
  await pending;
  reply(unwired());
  await controller.refresh();
  assert.deepEqual(states, [{ kind: "checking", diagnostic: "CHECKING" }]);
});
