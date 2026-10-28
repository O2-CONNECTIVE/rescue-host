import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, writeFile, rm } from 'node:fs/promises';
import { createHash } from 'node:crypto';
import { spawnSync } from 'node:child_process';
import path from 'node:path';
import os from 'node:os';

void test('CLIは一致buildを確認し、欠測traceを非0・秘密なしで返す', async () => {
  const directory = await mkdtemp(path.join(os.tmpdir(), 'rescue-startup-cli-'));
  try {
    const exe = path.join(directory, 'fixture.exe'); const manifest = path.join(directory, 'build.json');
    const bytes = Buffer.from('fixture-only-not-an-executable');
    await writeFile(exe, bytes);
    await writeFile(manifest, JSON.stringify({ schema: 1, variant: 'host-startup-diagnostic-1',
      sourceCommit: 'a'.repeat(40), features: ['custom-protocol', 'startup-diagnostics'], profile: 'release',
      exeSha256: createHash('sha256').update(bytes).digest('hex'), distribution: false }));
    const args = ['native-smoke/inspect-app-startup.ts', '--directory', directory, '--manifest', manifest, '--exe', exe, '--source', 'a'.repeat(40)];
    const run = (extra: string[]) => spawnSync(process.execPath, [...args, ...extra], { encoding: 'utf8' });
    const build = run(['--build-only']);
    assert.equal(build.status, 0, build.stderr); assert.equal(JSON.parse(build.stdout).code, 'APP_STARTUP_BUILD_VERIFIED');
    const missing = run([]);
    assert.equal(missing.status, 1); assert.equal(JSON.parse(missing.stdout).startup.phase, 'diagnostic-incomplete');
    assert.equal(missing.stderr, ''); assert.equal(missing.stdout.includes(directory), false);
    await writeFile(exe, 'PRIVATE_FIXTURE');
    const drift = run(['--build-only']);
    assert.equal(drift.status, 1); assert.equal(JSON.parse(drift.stdout).code, 'APP_STARTUP_BUILD_REJECTED');
    assert.equal((drift.stdout + drift.stderr).includes('PRIVATE_FIXTURE'), false);
  } finally { await rm(directory, { recursive: true, force: true }); }
});
