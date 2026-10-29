// 成功/失敗と別に、先行armが所有した資源の終了確認だけを後続条件とする。
export function ownedCleanupConfirmed(value: unknown, expected: unknown, mode: 'with-html' | 'load-html'): boolean {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false;
  if (!expected || typeof expected !== 'object' || Array.isArray(expected)) return false;
  const v = value as Record<string, unknown>;
  const e = expected as Record<string, unknown>;
  return v.schema === 1 && v.kind === 'direct-wry-controller-contrast' && v.fixedInputsVerified === true
    && v.mode === mode && typeof v.sourceCommit === 'string' && /^[0-9a-f]{40}$/.test(v.sourceCommit)
    && typeof v.exeSha256 === 'string' && /^[0-9a-f]{64}$/.test(v.exeSha256)
    && typeof v.runId === 'string' && /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/.test(v.runId)
    && v.sourceCommit === e.sourceCommit && v.exeSha256 === e.exeSha256 && v.runId === e.runId
    && v.ownerSetRecorded === true && Number.isSafeInteger(v.observedChildren) && Number(v.observedChildren) > 0
    && v.appExited === true && v.observedChildrenExited === true && v.driversExited === true
    && v.listenersAbsent === true && v.cleanupFailures === 0;
}
export function mayStartSecond(value: unknown, expected: unknown): boolean { return ownedCleanupConfirmed(value, expected, 'with-html'); }

type Outcome = 'passed' | 'failed' | 'timeout' | 'error';
type First = { outcome: Outcome; report: unknown };
type Second = { outcome: Outcome; report: unknown };
export async function runSequential(first: () => Promise<First>, second: () => Promise<Second>, expected: unknown) {
  let a: First;
  try { a = await first(); } catch { a = { outcome: 'error', report: null }; }
  if ((a.outcome !== 'passed' && a.outcome !== 'failed') || !mayStartSecond(a.report, expected)) {
    return { first: a.outcome, second: 'not-started', cleanup: 'unconfirmed', secondCleanup: 'not-started' };
  }
  let b: Second;
  try { b = await second(); } catch { b = { outcome: 'error', report: null }; }
  const clean = (b.outcome === 'passed' || b.outcome === 'failed') && ownedCleanupConfirmed(b.report, expected, 'load-html');
  return { first: a.outcome, second: b.outcome, cleanup: 'confirmed', secondCleanup: clean ? 'confirmed' : 'unconfirmed' };
}
