import { lstat, readFile, writeFile } from 'node:fs/promises';
import path from 'node:path';

const names = ['navigation-allowed', 'navigation-denied', 'navigation-denied-fixture', 'load-started', 'load-finished'] as const;
type Name = typeof names[number];
type Snapshot = { schema: 1; phase: string; dropped: number; events: Name[] };

function snapshot(value: unknown, phase: string): value is Snapshot {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false;
  const v = value as Record<string, unknown>;
  return v.schema === 1 && v.phase === phase && v.dropped === 0 && Array.isArray(v.events)
    && v.events.length <= 32 && v.events.every(x => typeof x === 'string' && names.includes(x as Name));
}

export function classifyNavigationSnapshots(before: unknown, after: unknown) {
  const valid = snapshot(before, 'before') && snapshot(after, 'after')
    && before.events.length <= after.events.length && before.events.every((x, i) => after.events[i] === x);
  const classify = (...selected: Name[]) => !valid ? 'unknown' : selected.some(name => before.events.includes(name)) ? 'before-request'
    : selected.some(name => after.events.includes(name)) ? 'by-after-snapshot' : 'unobserved';
  // channel滞留・snapshot後の取りこぼし・worker未終了は確定できない。
  return { diagnostic: 'incomplete', snapshotState: valid ? 'observed' : 'invalid', navigationAllowed: classify('navigation-allowed'),
    navigationDenied: classify('navigation-denied', 'navigation-denied-fixture'), fixtureUriMatch: classify('navigation-denied-fixture'),
    loadStarted: classify('load-started'), loadFinished: classify('load-finished') };
}
// 同一実行の専用dirだけを使用。ack未着時は待機を切り上げunknownとする。
export async function captureNavigationSnapshot(directory: string, phase: 'before' | 'after'): Promise<unknown> {
  const request = path.join(directory, `navigation-${phase}.request`);
  const result = path.join(directory, `navigation-${phase}.json`);
  try {
    await writeFile(request, '', { flag: 'wx' });
    const deadline = performance.now() + 500;
    while (performance.now() < deadline) {
      try {
        const stat = await lstat(result);
        if (!stat.isFile() || stat.isSymbolicLink() || stat.size > 8192) return null;
        return JSON.parse(await readFile(result, 'utf8')) as unknown;
      } catch { await new Promise(resolve => setTimeout(resolve, 10)); }
    }
  } catch { /* 作成失敗や旧markerは診断欠測。 */ }
  return null;
}
