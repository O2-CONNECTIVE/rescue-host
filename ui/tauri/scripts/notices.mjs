import { readFile, readdir, writeFile, realpath, lstat } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const root = await realpath(fileURLToPath(new URL('../', import.meta.url)));
async function contained(file) {
  const resolved = await realpath(file);
  const relative = path.relative(root, resolved);
  if (relative === '..' || relative.startsWith(`..${path.sep}`) || path.isAbsolute(relative)) throw new Error('Path outside root');
  return resolved;
}
async function licenseText(file) {
  const text = await readFile(await contained(file), 'utf8');
  if (!text.trim()) throw new Error('Empty license or notice');
  return text;
}
async function main() {
  const checking = process.argv[2] === '--check';
  if (process.argv.length > (checking ? 3 : 2)) throw new Error('Unexpected arguments');
  const lock = JSON.parse(await readFile(await contained(path.join(root, 'package-lock.json')), 'utf8'));
  if (lock.lockfileVersion !== 3 || !lock.packages || Array.isArray(lock.packages)) throw new Error('Invalid lock');
  const sections = ['Rescue Host frontend runtime third-party notices', await licenseText(path.join(root, 'third-party/LICENSE.shadcn'))];
  let count = 0;
  for (const [name, pkg] of Object.entries(lock.packages).sort(([a], [b]) => a < b ? -1 : a > b ? 1 : 0)) {
    if (!name || pkg.dev === true) continue;
    if (pkg.optional) throw new Error('Optional runtime needs an explicit distribution classification');
    if (!/^node_modules\/(?:@[a-z0-9._-]+\/)?[a-z0-9_-][a-z0-9._-]*(?:\/node_modules\/(?:@[a-z0-9._-]+\/)?[a-z0-9_-][a-z0-9._-]*)*$/.test(name)) throw new Error('Invalid runtime path');
    if (![pkg.version, pkg.license].every(value => typeof value === 'string' && value.trim() && !/[\r\n\0]/.test(value))) throw new Error('Invalid runtime metadata');
    count += 1;
    const folder = await contained(path.join(root, name));
    const files = (await readdir(folder)).filter(file => /^(licen[sc]e|copying|notice)(\.|$)/i.test(file)).sort();
    if (!files.some(file => /^(licen[sc]e|copying)(\.|$)/i.test(file))) throw new Error('Missing runtime license');
    sections.push(`\n=== ${name} @ ${pkg.version} (${pkg.license}) ===\n`);
    for (const file of files) sections.push(await licenseText(path.join(folder, file)));
  }
  if (!count) throw new Error('No runtime packages');
  const output = path.join(await contained(path.join(root, 'dist')), 'THIRD_PARTY_NOTICES.txt');
  if (checking) {
    if (await readFile(await contained(output), 'utf8') !== sections.join('\n')) throw new Error('Notice mismatch');
  } else {
    try {
      if (!(await lstat(output)).isFile()) throw new Error('Output must be a regular file');
    } catch (error) { if (error.code !== 'ENOENT') throw error; }
    await writeFile(output, sections.join('\n'));
  }
  console.log(`Frontend runtime notices: packages=${count}; owned=1; mode=${checking ? 'check' : 'generate'}`);
}
main().catch(() => { console.error('Frontend notice check failed; inspect lock, licenses and generated notice locally.'); process.exitCode = 1; });
