const stages = ['webview-enter', 'environment-reused', 'environment-api-start', 'environment-api-return',
  'environment-callback', 'environment-wait-return', 'controller-api-start', 'controller-api-return',
  'controller-callback', 'controller-wait-return', 'init-enter', 'init-return', 'webview-exit-ok', 'webview-exit-error', 'webview-unwind'] as const;
type Stage = typeof stages[number];
type Event = { seq: number; stage: Stage; elapsedUs: number };
function object(x: unknown): x is Record<string, unknown> { return !!x && typeof x === 'object' && !Array.isArray(x); }
function exact(x: Record<string, unknown>, keys: string[]) { return Object.keys(x).length === keys.length && keys.every(k => Object.hasOwn(x, k)); }
function integer(x: unknown): x is number { return typeof x === 'number' && Number.isSafeInteger(x) && x >= 0; }
export function classifyWryStartup(input: unknown) {
  const incomplete = { schema: 1, purpose: 'diagnostic-hints-only', phase: 'diagnostic-incomplete', events: [] as Event[], windowEndUs: null, pumps: null, pumpHint: null };
  if (!object(input) || !exact(input, ['schema', 'variant', 'purpose', 'events', 'attempted', 'recorded', 'dropped', 'sealed', 'inFlight', 'windowEndUs', 'pumps', 'scripts']) ||
    input.schema !== 1 || input.variant !== 'wry-webview2-stage-3' || input.purpose !== 'diagnostic-hints-only' ||
    !integer(input.windowEndUs) || input.sealed !== true || input.inFlight !== 0 || input.dropped !== 0 ||
    !Array.isArray(input.events) || input.events.length > 12 || input.recorded !== input.events.length || input.attempted !== input.events.length) return incomplete;
  const events: Event[] = []; const seen = new Set<Stage>(); let elapsed = 0;
  for (const row of input.events) {
    if (!object(row) || !exact(row, ['seq', 'stage', 'elapsedUs']) || row.seq !== events.length + 1 ||
      !integer(row.elapsedUs) || row.elapsedUs < elapsed) return incomplete;
    const stage = stages.find(s => s === row.stage);
    if (!stage || seen.has(stage) || ['webview-exit-ok', 'webview-exit-error', 'webview-unwind'].some(s => seen.has(s as Stage)) || (events.length === 0 && stage !== 'webview-enter') ||
      (stage !== 'webview-enter' && !seen.has('webview-enter'))) return incomplete;
    const environmentReady = seen.has('environment-reused') || seen.has('environment-wait-return');
    if ((stage === 'environment-reused' && seen.has('environment-api-start')) ||
      (stage === 'environment-api-start' && seen.has('environment-reused')) ||
      (['environment-api-return', 'environment-callback'].includes(stage) && !seen.has('environment-api-start')) ||
      (stage === 'environment-wait-return' && (!seen.has('environment-api-return') || !seen.has('environment-callback'))) ||
      (stage === 'controller-api-start' && !environmentReady) ||
      (['controller-api-return', 'controller-callback'].includes(stage) && !seen.has('controller-api-start')) ||
      (stage === 'controller-wait-return' && (!seen.has('controller-api-return') || !seen.has('controller-callback'))) ||
      (stage === 'init-enter' && !seen.has('controller-wait-return')) ||
      (stage === 'init-return' && !seen.has('init-enter')) ||
      (stage === 'webview-exit-ok' && !seen.has('init-return'))) return incomplete;
    seen.add(stage); events.push({ seq: events.length + 1, stage, elapsedUs: row.elapsedUs }); elapsed = row.elapsedUs;
  }
  if (input.windowEndUs < elapsed) return incomplete;
  const pumps = readPumps(input.pumps, input.windowEndUs); if (!pumps) return incomplete;
  for (const scope of ['environment', 'controller'] as const) {
    const pump = pumps[scope];
    const api = events.find(e => e.stage === `${scope}-api-return`);
    const returned = events.find(e => e.stage === `${scope}-wait-return`);
    const callback = events.find(e => e.stage === `${scope}-callback`);
    if ((pump.entered && (!api || pump.lastUs < api.elapsedUs))
      || (returned && (pump.phase !== 'receiver-ready' || pump.lastUs > returned.elapsedUs))
      || (pump.phase === 'receiver-ready' && (!callback || pump.lastUs < callback.elapsedUs))) return incomplete;
  }
  if (seen.has('environment-reused') && pumps.environment.entered) return incomplete;
  const scripts = readScripts(input.scripts, input.windowEndUs, pumps.script); if (!scripts) return incomplete;
  const init = events.find(e => e.stage === 'init-enter'); const initReturn = events.find(e => e.stage === 'init-return');
  if (scripts.calls > 0 && (!init || scripts.startedUs === null || scripts.startedUs < init.elapsedUs)) return incomplete;
  if (initReturn && scripts.calls > 0 && (scripts.returnUs === null || scripts.returnUs > initReturn.elapsedUs)) return incomplete;
  if (seen.has('webview-exit-ok') && scripts.calls > 0 && scripts.result !== 'ok') return incomplete;
  let phase = 'webview-entry-not-observed';
  if (seen.has('webview-enter')) {
    if (!seen.has('environment-reused') && !seen.has('environment-wait-return')) {
      phase = !seen.has('environment-api-start') ? 'environment-api-start-not-observed'
        : !seen.has('environment-api-return') ? 'environment-api-return-not-observed'
        : !seen.has('environment-callback') ? 'environment-callback-not-observed' : 'environment-wait-return-not-observed';
    } else phase = !seen.has('controller-api-start') ? 'controller-api-start-not-observed'
      : !seen.has('controller-api-return') ? 'controller-api-return-not-observed'
      : !seen.has('controller-callback') ? 'controller-callback-not-observed'
      : !seen.has('controller-wait-return') ? 'controller-wait-return-not-observed' : 'controller-wait-returned';
  }
  if (seen.has('webview-unwind')) phase = 'webview-unwind-observed';
  else if (seen.has('webview-exit-error')) phase = 'webview-error-exit-observed';
  else if (seen.has('webview-exit-ok')) phase = 'webview-exit-guard-ok-observed';
  else if (seen.has('init-return')) phase = 'post-init-webview-exit-not-observed';
  else if (seen.has('init-enter')) phase = scriptPhase(scripts);
  const hint = phase.startsWith('script-') && scripts.calls > 0 && scripts.result === 'pending'
    ? controllerPumpHint(pumps.script).replace('controller-', 'script-') : null;
  return { schema: 1, purpose: 'diagnostic-hints-only', phase, events, windowEndUs: input.windowEndUs, pumps, scripts,
    pumpHint: ['controller-callback-not-observed', 'controller-wait-return-not-observed'].includes(phase)
      ? controllerPumpHint(pumps.controller) : hint };
}
import { readPumps, controllerPumpHint } from './wry-pump-state.ts';
import { readScripts, scriptPhase } from './wry-script-state.ts';
