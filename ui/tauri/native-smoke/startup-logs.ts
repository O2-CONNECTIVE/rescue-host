import { lstat, open, writeFile } from 'node:fs/promises';
import path from 'node:path';

const limit = 65536;
const patterns = {
  proxyConnectionError: ['error serving connection:'],
  sessionNotCreated: ['session not created'],
  devtoolsPortMissing: ["devtoolsactiveport file doesn't exist"],
  profileInUse: ['user data directory is already in use'],
  browserNotReachable: ['browser not reachable', 'not connected to devtools'],
  browserExited: ['microsoft edge failed to start', 'browser has closed the connection'],
  versionMismatch: ['only supports microsoft edge version', 'current browser version is']
};
type Facts = {
  status: 'read' | 'unavailable';
  bytesRead: number | null;
  truncated: boolean | null;
  hints: Record<string, boolean> | null;
  proxyErrorKinds?: string[];
};
async function readFacts(file: string): Promise<Facts> {
  const unavailable: Facts = { status: 'unavailable', bytesRead: null, truncated: null, hints: null };
  let handle: Awaited<ReturnType<typeof open>> | undefined;
  try {
    // 自己所有CI領域の非競合が前提。検査からopenまでの敵対的差替えは隔離しない。
    const entry = await lstat(file);
    if (!entry.isFile() || entry.isSymbolicLink()) return unavailable;
    handle = await open(file, 'r');
    if (!(await handle.stat()).isFile()) return unavailable;
    const buffer = Buffer.alloc(limit);
    const { bytesRead } = await handle.read(buffer, 0, limit, 0);
    const text = buffer.subarray(0, bytesRead).toString('utf8').toLowerCase();
    const hints = Object.fromEntries(Object.entries(patterns).map(([name, values]) =>
      [name, values.some(value => text.includes(value))]));
    const kinds = [['incomplete-message', 'hyper::error(incompletemessage'], ['canceled', 'hyper::error(canceled'], ['io', 'hyper::error(io']];
    const proxyErrorKinds = kinds.filter(([, phrase]) => text.includes(phrase)).map(([kind]) => kind);
    if (hints.proxyConnectionError && proxyErrorKinds.length === 0) proxyErrorKinds.push('unclassified');
    return { status: 'read', bytesRead, truncated: (await handle.stat()).size > bytesRead, hints, proxyErrorKinds };
  } catch { return unavailable; }
  finally { await handle?.close(); }
}
export async function readStartupLogFacts(directory: string) {
  // 自己所有の固定2ファイルだけ。原文、filename、path、例外文はJSONへ複製しない。
  const [stdout, stderr] = await Promise.all([
    readFacts(path.join(directory, 'driver.stdout.log')),
    readFacts(path.join(directory, 'driver.stderr.log'))
  ]);
  return { schema: 1, purpose: 'diagnostic-hints-only', stdout, stderr };
}
export async function saveStartupLogFacts(directory: string, output: string): Promise<void> {
  await writeFile(path.join(output, 'driver-startup-log-facts.json'), JSON.stringify(await readStartupLogFacts(directory)));
}
