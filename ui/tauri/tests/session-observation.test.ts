import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, writeFile, readFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';

void test('単一sessionの中断前後を単調時刻と固定分類で保存し、元timeoutを保持する', async t => {
  const directory = await mkdtemp(path.join(tmpdir(), 'session-timing-'));
  t.after(() => rm(directory, { recursive: true, force: true }));
  await writeFile(path.join(directory, 'driver.stdout.log'), 'Error serving connection: hyper::Error(IncompleteMessage) PRIVATE_PATH');
  const { observeSessionStartup } = await import('../native-smoke/session-observation.ts');
  let now = 0;
  let fire: (() => void) | undefined;
  let calls = 0;
  let reject: ((error: Error) => void) | undefined;
  const timeout = new Error('NATIVE_DRIVER_TIMEOUT');
  const operation = new Promise<string>((_, no) => { reject = no; });
  const result = observeSessionStartup(() => { calls += 1; return operation; }, directory, directory, {
    now: () => now, schedule: callback => { fire = callback; return () => { fire = undefined; }; }
  });
  now = 10000;
  fire?.();
  now = 15000;
  reject?.(timeout);
  await assert.rejects(result, error => error === timeout);
  const raw = await readFile(path.join(directory, 'session-startup-timing.json'), 'utf8');
  const record: unknown = JSON.parse(raw);
  assert.ok(record !== null && typeof record === 'object' && 'settledMs' in record && 'outcome' in record && 'samples' in record);
  assert.equal(record.settledMs, 15000);
  assert.equal(record.outcome, 'timeout');
  assert.ok(Array.isArray(record.samples));
  const samples: unknown[] = record.samples;
  assert.equal(samples.length, 2);
  assert.ok(samples[0] !== null && typeof samples[0] === 'object' && 'requestPendingAtStart' in samples[0] && 'startedMs' in samples[0]);
  assert.equal(samples[0].requestPendingAtStart, true);
  assert.equal(samples[0].startedMs, 10000);
  assert.equal(raw.includes('incomplete-message'), true);
  assert.equal(raw.includes('PRIVATE_PATH'), false);
  assert.equal(raw.includes(directory), false);
  assert.equal(calls, 1);
  assert.equal(fire, undefined);
});

void test('早期成功はtimerを解除し、存在しないログを読まず元の結果を返す', async t => {
  // 実装後回帰。先行REDとは区別する。
  const directory = await mkdtemp(path.join(tmpdir(), 'session-success-'));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const { observeSessionStartup } = await import('../native-smoke/session-observation.ts');
  let canceled = false;
  assert.equal(await observeSessionStartup(() => Promise.resolve('original'), directory, directory, {
    now: () => 7, schedule: () => () => { canceled = true; }
  }), 'original');
  const record: unknown = JSON.parse(await readFile(path.join(directory, 'session-startup-timing.json'), 'utf8'));
  assert.deepEqual(record, { schema: 1, purpose: 'diagnostic-hints-only', settledMs: 0, outcome: 'success', samples: [] });
  assert.equal(canceled, true);
});

void test('診断保存不能でも元の拒否を置換せず、固定codeだけを出力する', async t => {
  // 実装後回帰。原例外のidentityも維持する。
  const directory = await mkdtemp(path.join(tmpdir(), 'session-write-failure-'));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const { observeSessionStartup } = await import('../native-smoke/session-observation.ts');
  const codes: unknown[][] = [];
  t.mock.method(console, 'error', (...args: unknown[]) => { codes.push(args); });
  const original = new Error('PRIVATE_FAILURE');
  await assert.rejects(observeSessionStartup(() => Promise.reject(original), directory, path.join(directory, 'missing'), {
    now: () => 1, schedule: () => () => {}
  }), error => error === original);
  assert.deepEqual(codes, [['NATIVE_SESSION_TIMING_EVIDENCE_FAILED']]);
});
