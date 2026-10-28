import { open, lstat } from 'node:fs/promises';
import path from 'node:path';
import { classifyAppStartup } from './app-startup-stages.ts';

export async function inspectAppStartupFile(directory: string) {
  let file: Awaited<ReturnType<typeof open>> | undefined;
  try {
    const filename = path.join(directory, 'app-startup-stages.json');
    const stat = await lstat(filename);
    if (!stat.isFile() || stat.isSymbolicLink() || stat.size > 2048) return classifyAppStartup(null);
    file = await open(filename, 'r');
    const buffer = Buffer.alloc(2049);
    const { bytesRead } = await file.read(buffer, 0, buffer.length, 0);
    if (bytesRead > 2048) return classifyAppStartup(null);
    const text = new TextDecoder('utf-8', { fatal: true }).decode(buffer.subarray(0, bytesRead));
    const value: unknown = JSON.parse(text);
    // Rustの固定compact出力だけを受け入れ、重複fieldや途中書込みを拒否する。
    return classifyAppStartup(JSON.stringify(value) === text ? value : null);
  } catch { return classifyAppStartup(null); }
  finally { await file?.close(); }
}
