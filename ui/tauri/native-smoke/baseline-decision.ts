// 独立対照の結果だけを判定し、製品合格を返さない。
function object(x: unknown): x is Record<string, unknown> { return !!x && typeof x === 'object' && !Array.isArray(x); }
export function decideBaseline(collection: unknown, wry: unknown, session: unknown) {
  const base = { schema: 1, purpose: 'diagnostic-only', productNativePass: false, rootCauseProven: false,
    observedChildrenOnly: true, contrastPassed: false, cleanupVerified: false };
  const incomplete = { ...base, diagnostic: 'incomplete', outcome: 'inconclusive', nextAction: 'repair-observation' };
  if (!object(collection) || collection.schema !== 1 || collection.kind !== 'direct-wry-controller-contrast'
    || collection.productNativePass !== false || collection.fixedInputsVerified !== true
    || collection.wryStartupFileClosed !== true || !object(wry) || wry.diagnostic !== 'complete'
    || !object(wry.startup) || !Array.isArray(wry.startup.events)) return incomplete;
  const cleanupVerified = collection.cleanupFailures === 0 && collection.ownerSetRecorded === true
    && Number.isSafeInteger(collection.observedChildren) && Number(collection.observedChildren) > 0
    && collection.observedChildrenExited === true && collection.appExited === true
    && collection.driversExited === true && collection.listenersAbsent === true;
  const result = { ...base, diagnostic: 'complete', cleanupVerified };
  const stages = wry.startup.events.filter(object).map(e => e.stage);
  const exited = wry.startup.phase === 'webview-exit-guard-ok-observed' && stages.includes('controller-callback')
    && stages.includes('controller-wait-return') && stages.includes('webview-exit-ok');
  if (exited && collection.sessionSucceeded === true && object(session) && session.schema === 1
    && session.kind === 'direct-wry-controller-contrast' && session.productNativePass === false
    && session.sessionCreated === true && session.documentChecked === true && session.browserVersion === '131.0.2903.86'
    && collection.normalCloseVerified === true && collection.driverStableThroughClose === true && collection.forcedApp === false && collection.forcedChildren === 0 && cleanupVerified) {
    return { ...result, contrastPassed: true, outcome: 'contrast-pass', nextAction: 'compare-context-settings' };
  }
  if (object(session) && session.sessionCreated === true && session.browserVersion !== '131.0.2903.86') {
    return { ...result, outcome: 'runtime-mismatch', nextAction: 'repair-fixed-environment' };
  }
  if (object(session) && session.sessionCreated === true && session.documentChecked !== true) {
    return { ...result, outcome: 'document-not-verified', nextAction: 'inspect-document-boundary' };
  }
  if (object(session) && session.sessionCreated === false && session.sessionOutcome === 'request-failed') {
    return { ...result, outcome: 'session-request-failed', nextAction: 'inspect-driver-request' };
  }
  const nextAction = cleanupVerified ? 'compare-controller-context' : 'repair-ownership-observation';
  if (wry.startup.phase === 'controller-callback-not-observed' && stages.includes('controller-api-return')
    && !stages.includes('controller-callback') && collection.sessionSucceeded === false
    && collection.sessionTimedOut === true && object(session) && session.sessionCreated === false && session.sessionOutcome === 'timeout') {
    return { ...result, outcome: 'controller-callback-unobserved', nextAction };
  }
  return { ...result, outcome: exited && collection.sessionSucceeded === false ? 'session-after-wry-exit' : 'other-boundary',
    nextAction: cleanupVerified ? 'inspect-next-boundary' : 'repair-ownership-observation' };
}
