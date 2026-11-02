import { constants } from 'node:fs';
import { cp, copyFile, lstat, mkdir, readFile } from 'node:fs/promises';
import { createHash } from 'node:crypto';
import path from 'node:path';

async function digest(name: string) {
  const stat = await lstat(name);
  if (!stat.isFile() || stat.isSymbolicLink() || stat.size > 64 * 1024 * 1024) throw new Error();
  const data = await readFile(name); if (data.length > 64 * 1024 * 1024) throw new Error();
  return createHash('sha256').update(data).digest('hex');
}
// 実行済みarmのprofile・ログを共有せず、固定toolだけを新規領域へ複製する。
export async function copyBaselineTools(source: string, target: string, version: string) {
  if (!/^\d+\.\d+\.\d+$/.test(version)) throw new Error();
  await mkdir(target); await mkdir(path.join(target, 'edge')); await mkdir(path.join(target, 'tauri', 'bin'), { recursive: true });
  const files = ['edge/msedgedriver.exe', 'tauri/bin/tauri-driver.exe', 'driver-provenance.json', 'tauri-driver.crate'];
  for (const name of files) {
    const from = path.join(source, name), to = path.join(target, name); const before = await digest(from);
    await copyFile(from, to, constants.COPYFILE_EXCL);
    if (await digest(to) !== before) throw new Error();
  }
  const sourceTree = `tauri-driver-${version}`;
  await cp(path.join(source, sourceTree), path.join(target, sourceTree), { recursive: true, errorOnExist: true, force: false });
}
