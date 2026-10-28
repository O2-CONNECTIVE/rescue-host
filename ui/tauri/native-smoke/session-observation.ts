import { writeFile } from 'node:fs/promises';
import path from 'node:path';
import { readStartupLogFacts } from './startup-logs.ts';

type Clock = { now: () => number; schedule: (callback: () => void) => () => void };
const clock: Clock = {
  now: () => performance.now(),
  schedule: callback => { const timer = setTimeout(callback, 10000); return () => clearTimeout(timer); }
};
function outcome(error: unknown): string {
  if (error instanceof Error && (error.message === 'NATIVE_DRIVER_TIMEOUT' || error.name === 'TimeoutError' || error.name === 'AbortError')) return 'timeout';
  return error instanceof Error && error.message === 'NATIVE_DRIVER_TRANSPORT_FAILED' ? 'transport' : 'other';
}
export async function observeSessionStartup<T>(operation: () => Promise<T>, directory: string, output: string, time: Clock = clock): Promise<T> {
  const start = time.now();
  let pending = true;
  const samples: unknown[] = [];
  const elapsed = () => Math.max(0, Math.round(time.now() - start));
  const capture = async (stage: string) => {
    const startedMs = elapsed();
    const requestPendingAtStart = pending;
    let facts: unknown = null;
    try { facts = await readStartupLogFacts(directory); } catch { /* 固定null。原例外を複製しない。 */ }
    samples.push({ stage, startedMs, completedMs: elapsed(), requestPendingAtStart, facts });
  };
  let early: Promise<void> | undefined;
  const cancel = time.schedule(() => { early = capture('pending-timer'); });
  let status = 'success';
  try { return await operation(); }
  catch (error) { status = outcome(error); throw error; }
  finally {
    const settledMs = elapsed();
    pending = false;
    cancel();
    // 観測は同じ要求の結果を変更しない。request期限はDriver側の既存15秒のまま。
    try {
      await early;
      if (status !== 'success') await capture('after-settlement');
      await writeFile(path.join(output, 'session-startup-timing.json'), JSON.stringify({ schema: 1, purpose: 'diagnostic-hints-only', settledMs, outcome: status, samples }));
    } catch { console.error('NATIVE_SESSION_TIMING_EVIDENCE_FAILED'); }
  }
}
