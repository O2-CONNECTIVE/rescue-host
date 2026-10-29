import { open, lstat } from 'node:fs/promises';
import path from 'node:path';
import { classifyWryStartup } from './wry-startup-stages.ts';
export async function inspectWryStartupFile(directory: string, collection: unknown) {
  let file: Awaited<ReturnType<typeof open>> | undefined;
  try {
    if (!collection || typeof collection !== 'object' || !('kind' in collection)
      || !('wryStartupFileClosed' in collection) || collection.wryStartupFileClosed !== true) return classifyWryStartup(null);
    const native = collection.kind === 'windows-webview2-native-smoke';
    const contrast = collection.kind === 'direct-wry-controller-contrast' && 'schema' in collection && collection.schema === 1
      && 'productNativePass' in collection && collection.productNativePass === false;
    if (!native && !contrast) return classifyWryStartup(null);
    const name = path.join(directory, 'wry-startup-stages.json'); const stat = await lstat(name);
    if (!stat.isFile() || stat.isSymbolicLink() || stat.size > 2048) return classifyWryStartup(null);
    file = await open(name, 'r'); const buffer = Buffer.alloc(2049);
    const { bytesRead } = await file.read(buffer, 0, buffer.length, 0);
    if (bytesRead > 2048) return classifyWryStartup(null);
    const text = new TextDecoder('utf-8', { fatal: true }).decode(buffer.subarray(0, bytesRead));
    const input: unknown = JSON.parse(text);
    return classifyWryStartup(JSON.stringify(input) === text ? input : null);
  } catch { return classifyWryStartup(null); }
  finally { await file?.close(); }
}
