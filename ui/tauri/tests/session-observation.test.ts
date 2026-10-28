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
