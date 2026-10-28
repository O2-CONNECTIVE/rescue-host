import { open, lstat } from 'node:fs/promises';
import path from 'node:path';
import { classifyAppStartup } from './app-startup-stages.ts';

export function verifyStartupBuild(input: unknown, source: string, sha256: string): boolean {
  if (!input || typeof input !== 'object' || Array.isArray(input)) return false;
  const value = input as Record<string, unknown>;
  const keys = ['schema', 'variant', 'sourceCommit', 'features', 'profile', 'exeSha256', 'distribution'];
  return Object.keys(value).length === keys.length && keys.every(key => Object.hasOwn(value, key))
    && value.schema === 1 && value.variant === 'host-startup-diagnostic-1' && value.distribution === false
    && /^[0-9a-f]{40}$/.test(source) && /^[0-9a-f]{64}$/.test(sha256)
    && value.sourceCommit === source && value.exeSha256 === sha256
    && (value.profile === 'debug' || value.profile === 'release')
    && Array.isArray(value.features) && value.features.length === 2
    && value.features[0] === 'custom-protocol' && value.features[1] === 'startup-diagnostics';
}

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
