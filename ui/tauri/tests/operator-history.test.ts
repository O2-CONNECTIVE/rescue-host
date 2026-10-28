import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createMemoryHistory } from '../src/operator/history.ts';

void test('履歴は自前の日時と表示名・終了結果だけをメモリに保存する', () => {
  const history = createMemoryHistory({ now: () => 1_000 });
  history.record('練習用PC 01', 'unconfirmed');
  assert.equal(history.mode, 'memory_mock');
  assert.deepEqual(history.entries(), [
    { displayName: '練習用PC 01', occurredAt: 1_000, result: 'unconfirmed' }
  ]);
});

// 追加回帰: 既存のGREENに対して時計巻戻り時のmockの制限を固定する。
void test('時計の巻戻りで未来扱いになった履歴は記録時刻から30日後のpruneで消える', () => {
  let now = 1_000;
  const history = createMemoryHistory({ now: () => now });
  history.record('PC', 'confirmed');
  const snapshot = history.entries();
  now = 0;
  history.prune();
  assert.equal(history.entries(), snapshot);
  now = 2_592_001_000;
  assert.equal(history.entries(), snapshot);
  history.prune();
  assert.deepEqual(history.entries(), []);
});

// 追加回帰: 変更のない操作とstoreの独立性。
void test('変更のない操作と別storeの項目削除は通知せず、store同士は独立する', () => {
  let now = 0;
  const history = createMemoryHistory({ now: () => now });
  const other = createMemoryHistory({ now: () => now });
  other.record('PC', 'unconfirmed');
  let changes = 0;
  history.subscribe(() => { changes += 1; });
  history.clear();
  history.remove(other.entries()[0]);
  const update = history.record('PC', 'unconfirmed');
  update('unconfirmed');
  assert.equal(changes, 1);
  const entry = history.entries()[0];
  history.remove(entry);
  history.remove(entry);
  now = NaN;
  update('confirmed');
  history.clear();
  assert.equal(changes, 2);
  assert.equal(other.entries().length, 1);
});

void test('不正な時計値は変更前に拒否し、entriesの読取りは時計を呼ばない', () => {
  let now = 10;
  const history = createMemoryHistory({ now: () => now });
  const update = history.record('PC', 'unconfirmed');
  const snapshot = history.entries();
  let changes = 0;
  history.subscribe(() => { changes += 1; });
  for (const invalid of [NaN, Infinity, -Infinity, -1, 0.5, Number.MAX_SAFE_INTEGER + 1]) {
    now = invalid;
    assert.throws(() => history.record('追加PC', 'failed'), RangeError);
    assert.throws(() => history.prune(), RangeError);
    assert.throws(() => update('confirmed'), RangeError);
    assert.equal(history.entries(), snapshot);
    assert.equal(changes, 0);
  }
});

void test('recordの直前に期限切れを削除し、期限後の遅延更新でも再作成しない', () => {
  let now = 0;
  const history = createMemoryHistory({ now: () => now });
  const updateExpired = history.record('期限前のPC', 'unconfirmed');
  now = 2_592_000_000;
  history.record('新規PC', 'failed');
  updateExpired('confirmed');
  assert.deepEqual(history.entries(), [
    { displayName: '新規PC', occurredAt: 2_592_000_000, result: 'failed' }
  ]);
  const updateLater = history.record('遅延PC', 'unconfirmed');
  now = 5_184_000_000;
  updateLater('confirmed');
  assert.deepEqual(history.entries(), []);
});

void test('同じ名前の履歴も個別に更新し、不変snapshotを返し、削除後は復活しない', () => {
  const history = createMemoryHistory({ now: () => 1_000 });
  const updateFirst = history.record('同じPC', 'unconfirmed');
  const firstSnapshot = history.entries();
  const firstEntry = firstSnapshot[0];
  history.record('同じPC', 'failed');
  updateFirst('confirmed');
  assert.deepEqual(firstSnapshot, [
    { displayName: '同じPC', occurredAt: 1_000, result: 'unconfirmed' }
  ]);
  assert.deepEqual(history.entries().map(entry => entry.result), ['confirmed', 'failed']);
  assert.ok(Object.isFrozen(firstSnapshot));
  assert.ok(Object.isFrozen(firstEntry));
  assert.ok(Object.isFrozen(history.entries()));
  assert.ok(Object.isFrozen(history.entries()[0]));
  // 更新前に描画されたsnapshotの削除でも、同じ項目を削除する。
  history.remove(firstEntry);
  updateFirst('expired');
  assert.deepEqual(history.entries().map(entry => entry.result), ['failed']);
  const updateLast = history.record('別のPC', 'unconfirmed');
  history.clear();
  updateLast('confirmed');
  assert.deepEqual(history.entries(), []);
});

void test('30日ちょうどでpruneし、削除がないpruneや読取りは通知しない', () => {
  let now = 0;
  const history = createMemoryHistory({ now: () => now });
  let changes = 0;
  const unsubscribe = history.subscribe(() => { changes += 1; });
  history.record('古いPC', 'unconfirmed');
  now = 1;
  history.record('新しいPC', 'confirmed');
  const before = changes;
  now = 2_591_999_999;
  history.prune();
  history.entries();
  assert.equal(changes, before);
  assert.equal(history.entries().length, 2);
  now = 2_592_000_000;
  history.prune();
  assert.deepEqual(history.entries(), [
    { displayName: '新しいPC', occurredAt: 1, result: 'confirmed' }
  ]);
  assert.equal(changes, before + 1);
  unsubscribe();
  now += 1;
  history.prune();
  assert.deepEqual(history.entries(), []);
  assert.equal(changes, before + 1);
});
