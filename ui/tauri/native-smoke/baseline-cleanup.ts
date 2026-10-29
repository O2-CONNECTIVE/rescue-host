import { lstat, rm } from 'node:fs/promises';
import path from 'node:path';
import { ownedCleanupConfirmed } from './baseline-sequential.ts';

// 信頼された専用CI temp内の固定名だけ。未知のprocessや他の領域は触らない。
export async function cleanupBaselineArms(root: string, expected: unknown, first: unknown, second: unknown) {
  if (!path.isAbsolute(root)) throw new Error();
  const base = await lstat(root); if (!base.isDirectory() || base.isSymbolicLink()) throw new Error();
  async function remove(mode: 'with-html' | 'load-html', report: unknown, names: string[]) {
    if (!ownedCleanupConfirmed(report, expected, mode)) return 'unconfirmed';
    try {
      for (const name of names) { const stat = await lstat(path.join(root, name)); if (!stat.isDirectory() || stat.isSymbolicLink()) return 'unconfirmed'; }
      for (const name of names) await rm(path.join(root, name), { recursive: true });
      for (const name of names) { try { await lstat(path.join(root, name)); return 'unconfirmed'; } catch (error) { if ((error as NodeJS.ErrnoException).code !== 'ENOENT') return 'unconfirmed'; } }
      return 'removed';
    } catch { return 'unconfirmed'; }
  }
  return { first: await remove('with-html', first, ['baseline-tools', 'baseline-a-evidence']),
    second: await remove('load-html', second, ['baseline-tools-b', 'baseline-b-evidence']) };
}

// 両armが確認済または後続未作成のとき、同じrunnerで今回生成した固定名だけを片付ける。
export async function cleanupBaselineShared(root: string, arms: { first: string; second: string }) {
  if (arms.first !== 'removed' || !['removed', 'not-started'].includes(arms.second) || !path.isAbsolute(root)) return 'unconfirmed';
  const directories = ['baseline-project', 'baseline-target', 'wry-source'];
  const files = ['wry.crate', 'pump.crate', 'macro.crate', 'wry-prepared.json', 'wry-provenance.json', 'baseline-build.json',
    'baseline-feature-check.json', 'baseline-provenance.json', 'baseline-inventory.json', 'baseline-sequential.json'];
  try {
    const base = await lstat(root); if (!base.isDirectory() || base.isSymbolicLink()) return 'unconfirmed';
    for (const [name, directory] of [...directories.map(x => [x, true] as const), ...files.map(x => [x, false] as const)]) {
      try { const stat = await lstat(path.join(root, name)); if (stat.isSymbolicLink() || (directory ? !stat.isDirectory() : !stat.isFile())) return 'unconfirmed'; }
      catch (error) { if ((error as NodeJS.ErrnoException).code !== 'ENOENT') return 'unconfirmed'; }
    }
    for (const name of directories) await rm(path.join(root, name), { recursive: true, force: true });
    for (const name of files) await rm(path.join(root, name), { force: true });
    for (const name of [...directories, ...files]) { try { await lstat(path.join(root, name)); return 'unconfirmed'; } catch (error) { if ((error as NodeJS.ErrnoException).code !== 'ENOENT') return 'unconfirmed'; } }
    return 'removed';
  } catch { return 'unconfirmed'; }
}
