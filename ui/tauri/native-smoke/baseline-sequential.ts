// 成功/失敗と別に、先行armが所有した資源の終了確認だけを後続条件とする。
export function mayStartSecond(value: unknown): boolean {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false;
  const v = value as Record<string, unknown>;
  return v.schema === 1 && v.kind === 'direct-wry-controller-contrast' && v.fixedInputsVerified === true
    && v.mode === 'with-html' && typeof v.sourceCommit === 'string' && /^[0-9a-f]{40}$/.test(v.sourceCommit)
    && typeof v.exeSha256 === 'string' && /^[0-9a-f]{64}$/.test(v.exeSha256)
    && v.ownerSetRecorded === true && Number.isSafeInteger(v.observedChildren) && Number(v.observedChildren) > 0
    && v.appExited === true && v.observedChildrenExited === true && v.driversExited === true
    && v.listenersAbsent === true && v.cleanupFailures === 0;
}
