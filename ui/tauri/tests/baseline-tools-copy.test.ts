import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, readFile, rm, writeFile, access } from 'node:fs/promises';
import path from 'node:path';
import { tmpdir } from 'node:os';
import { copyBaselineTools } from '../native-smoke/baseline-tools-copy.ts';

void test('固定toolだけを新規dirへ複製し、先行profileやログを共有しない', async () => {
  const root = await mkdtemp(path.join(tmpdir(), 'baseline-tools-'));
  try {
    const source = path.join(root, 'a'), target = path.join(root, 'b');
    for (const name of ['edge', 'tauri/bin', 'tauri-driver-2.1.0', 'baseline-profile']) await mkdir(path.join(source, name), { recursive: true });
    for (const name of ['edge/msedgedriver.exe', 'tauri/bin/tauri-driver.exe', 'tauri-driver.crate', 'driver-provenance.json', 'tauri-driver-2.1.0/Cargo.toml', 'baseline-profile/private', 'driver.stdout.log']) await writeFile(path.join(source, name), name);
    await copyBaselineTools(source, target, '2.1.0');
    assert.equal(await readFile(path.join(target, 'edge/msedgedriver.exe'), 'utf8'), 'edge/msedgedriver.exe');
    await assert.rejects(access(path.join(target, 'baseline-profile')));
    await assert.rejects(access(path.join(target, 'driver.stdout.log')));
    await assert.rejects(copyBaselineTools(source, target, '2.1.0'));
    await assert.rejects(copyBaselineTools(source, path.join(root, 'other'), '../bad'));
  } finally { await rm(root, { recursive: true, force: true }); }
});
