import { readFile, readdir, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('../', import.meta.url));
const lock = JSON.parse(await readFile(path.join(root, 'package-lock.json'), 'utf8'));
const sections = ['Rescue Host frontend runtime third-party notices', await readFile(path.join(root, 'third-party/LICENSE.shadcn'), 'utf8')];
for (const [name, pkg] of Object.entries(lock.packages)) {
  if (!name || pkg.dev || pkg.optional) continue;
  const folder = path.join(root, name);
  const files = (await readdir(folder)).filter(file => /^(licen[sc]e|copying|notice)(\.|$)/i.test(file)).sort();
  if (!files.some(file => /^(licen[sc]e|copying)(\.|$)/i.test(file))) throw new Error(`Missing runtime license: ${name}`);
  sections.push(`\n=== ${name} @ ${pkg.version} (${pkg.license}) ===\n`);
  for (const file of files) sections.push(await readFile(path.join(folder, file), 'utf8'));
}
await writeFile(path.join(root, 'dist/THIRD_PARTY_NOTICES.txt'), sections.join('\n'));
console.log('Embedded complete runtime npm and owned shadcn license/notice texts.');
