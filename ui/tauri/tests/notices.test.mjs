import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, copyFile, writeFile, readFile, rm, symlink } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { spawnSync } from 'node:child_process';

async function fixture(t) {
  const root = await mkdtemp(path.join(tmpdir(), 'notice-fixture-'));
  t.after(() => rm(root, { recursive: true, force: true }));
  for (const dir of ['scripts', 'third-party', 'dist', 'node_modules/demo']) await mkdir(path.join(root, dir), { recursive: true });
  await copyFile(new URL('../scripts/notices.mjs', import.meta.url), path.join(root, 'scripts/notices.mjs'));
  const lock = { lockfileVersion: 3, packages: { '': { dependencies: { demo: '1.0.0' } }, 'node_modules/demo': { version: '1.0.0', license: 'MIT' } } };
  const put = (file, text) => writeFile(path.join(root, file), text);
  await put('package-lock.json', JSON.stringify(lock));
  await put('third-party/LICENSE.shadcn', 'Owned fixture permission notice\n');
  await put('node_modules/demo/LICENSE', 'Demo copyright and permission\n');
  await put('node_modules/demo/NOTICE', 'Demo attribution\n');
  const run = (...args) => spawnSync(process.execPath, [path.join(root, 'scripts/notices.mjs'), ...args], { cwd: tmpdir(), encoding: 'utf8', timeout: 10000 });
  return { root, lock, put, run, output: () => readFile(path.join(root, 'dist/THIRD_PARTY_NOTICES.txt'), 'utf8') };
}

test('check rejects a missing generated notice without recreating it', async t => {
  const f = await fixture(t);
  assert.equal(f.run('--check').status, 1);
  await assert.rejects(f.output(), { code: 'ENOENT' });
});

test('generate preserves full license/NOTICE text and check is read only', async t => {
  const f = await fixture(t);
  assert.equal(f.run().status, 0);
  const expected = 'Rescue Host frontend runtime third-party notices\nOwned fixture permission notice\n\n\n=== node_modules/demo @ 1.0.0 (MIT) ===\n\nDemo copyright and permission\n\nDemo attribution\n';
  assert.equal(await f.output(), expected);
  assert.equal(f.run('--check').status, 0);
  assert.equal(await f.output(), expected);
  await f.put('dist/THIRD_PARTY_NOTICES.txt', 'changed');
  assert.equal(f.run('--check').status, 1);
  assert.equal(await f.output(), 'changed');
});

test('empty license text is rejected before generating output', async t => {
  const f = await fixture(t);
  await f.put('node_modules/demo/LICENSE', ' \n');
  assert.equal(f.run().status, 1);
  await assert.rejects(f.output(), { code: 'ENOENT' });
});

test('zero runtime packages is not a successful notice inventory', async t => {
  const f = await fixture(t);
  delete f.lock.packages['node_modules/demo'];
  await f.put('package-lock.json', JSON.stringify(f.lock));
  assert.equal(f.run().status, 1);
});

test('runtime metadata must identify a version and license', async t => {
  const f = await fixture(t);
  delete f.lock.packages['node_modules/demo'].license;
  await f.put('package-lock.json', JSON.stringify(f.lock));
  assert.equal(f.run().status, 1);
});

test('lock entries outside npm package paths are rejected', async t => {
  const f = await fixture(t);
  f.lock.packages['third-party'] = f.lock.packages['node_modules/demo'];
  delete f.lock.packages['node_modules/demo'];
  await f.put('package-lock.json', JSON.stringify(f.lock));
  assert.equal(f.run().status, 1);
});

test('package link outside the fixture root is rejected without leaking its text', async t => {
  const f = await fixture(t);
  const outside = await mkdtemp(path.join(tmpdir(), 'notice-outside-'));
  t.after(() => rm(outside, { recursive: true, force: true }));
  await writeFile(path.join(outside, 'LICENSE'), 'PRIVATE_FIXTURE_MARKER');
  await rm(path.join(f.root, 'node_modules/demo'), { recursive: true });
  await symlink(outside, path.join(f.root, 'node_modules/demo'), 'junction');
  const result = f.run();
  assert.equal(result.status, 1);
  assert.ok(!`${result.stdout}${result.stderr}`.includes('PRIVATE_FIXTURE_MARKER'));
  assert.ok(!`${result.stdout}${result.stderr}`.includes(outside));
});

test('unclassified optional runtime packages fail closed instead of disappearing', async t => {
  const f = await fixture(t);
  f.lock.packages['node_modules/optional-demo'] = { version: '1.0.0', license: 'MIT', optional: true };
  await f.put('package-lock.json', JSON.stringify(f.lock));
  assert.equal(f.run().status, 1);
});

test('lock version change makes generated notice stale until regenerated', async t => {
  const f = await fixture(t);
  assert.equal(f.run().status, 0);
  const before = await f.output();
  f.lock.packages['node_modules/demo'].version = '2.0.0';
  await f.put('package-lock.json', JSON.stringify(f.lock));
  assert.equal(f.run('--check').status, 1);
  assert.equal(await f.output(), before);
  assert.equal(f.run().status, 0);
  assert.match(await f.output(), /demo @ 2\.0\.0/);
  assert.equal(f.run('--check').status, 0);
});

test('empty owned shadcn license cannot be hidden by nonempty npm licenses', async t => {
  const f = await fixture(t);
  await f.put('third-party/LICENSE.shadcn', '\n');
  assert.equal(f.run().status, 1);
});

test('missing runtime LICENSE is rejected even when NOTICE exists', async t => {
  const f = await fixture(t);
  await rm(path.join(f.root, 'node_modules/demo/LICENSE'));
  assert.equal(f.run().status, 1);
});

test('dev-only packages are excluded while nested scoped runtime licenses are retained', async t => {
  const f = await fixture(t);
  f.lock.packages['node_modules/dev-only'] = { version: '1.0.0', dev: true, optional: true };
  f.lock.packages['node_modules/demo/node_modules/@fixture/child'] = { version: '3.0.0', license: 'BSD-2-Clause' };
  await mkdir(path.join(f.root, 'node_modules/demo/node_modules/@fixture/child'), { recursive: true });
  await f.put('node_modules/demo/node_modules/@fixture/child/COPYING', 'Child permission\n');
  await f.put('package-lock.json', JSON.stringify(f.lock));
  const result = f.run();
  assert.equal(result.status, 0);
  assert.match(result.stdout, /packages=2; owned=1/);
  assert.match(await f.output(), /@fixture\/child @ 3\.0\.0 \(BSD-2-Clause\)/);
  assert.match(await f.output(), /Child permission/);
  assert.equal(f.run('--check').status, 0);
});

test('unknown arguments fail without printing paths or fixture text', async t => {
  const f = await fixture(t);
  const result = f.run('--unknown');
  assert.equal(result.status, 1);
  assert.ok(!result.stderr.includes(f.root));
  assert.ok(!result.stderr.includes('Demo copyright'));
});

test('dangling output link cannot create a file outside the fixture root', async t => {
  const f = await fixture(t);
  const outside = await mkdtemp(path.join(tmpdir(), 'notice-output-'));
  t.after(() => rm(outside, { recursive: true, force: true }));
  const target = path.join(outside, 'not-created.txt');
  await symlink(target, path.join(f.root, 'dist/THIRD_PARTY_NOTICES.txt'), 'file');
  assert.equal(f.run().status, 1);
  await assert.rejects(readFile(target), { code: 'ENOENT' });
});
