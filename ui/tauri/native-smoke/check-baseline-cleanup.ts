import { lstat, readFile } from 'node:fs/promises';
import { mayStartSecond } from './baseline-sequential.ts';

let allowed = false;
try {
  if (process.argv.length !== 3) throw new Error();
  const name = process.argv[2]; const stat = await lstat(name);
  if (!stat.isFile() || stat.isSymbolicLink() || stat.size > 65536) throw new Error();
  const data = await readFile(name);
  if (data.length > 65536) throw new Error();
  allowed = mayStartSecond(JSON.parse(data.toString('utf8')) as unknown);
} catch { /* 生のpath、例外、入力を出力しない。 */ }
console.log(allowed ? 'BASELINE_CLEANUP_CONFIRMED' : 'BASELINE_CLEANUP_UNCONFIRMED');
process.exitCode = allowed ? 0 : 1;
