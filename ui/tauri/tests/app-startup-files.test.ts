import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, writeFile, rm } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { inspectAppStartupFile } from '../native-smoke/app-startup-files.ts';

void test('所有trace fileを有限に読み、page-load済みと欠測を匿名reportへ分ける', async () => {
  const directory = await mkdtemp(path.join(os.tmpdir(), 'rescue-startup-reader-'));
  try {
    const missing = await inspectAppStartupFile(directory);
    assert.equal(missing.phase, 'diagnostic-incomplete');
    const stages = ['setup-enter', 'webview-build-start', 'webview-build-ok', 'setup-return', 'first-page-load-finished'];
    await writeFile(path.join(directory, 'app-startup-stages.json'), JSON.stringify({
      schema: 1, variant: 'host-startup-stage-1', purpose: 'diagnostic-hints-only',
      events: stages.map((stage, i) => ({ seq: i + 1, stage, elapsedUs: i * 100 })),
      attempted: 5, recorded: 5, dropped: 0, sealed: true, inFlight: 0
    }));
    assert.equal((await inspectAppStartupFile(directory)).phase, 'first-page-load-finished');
  } finally { await rm(directory, { recursive: true, force: true }); }
});
