const stages = ['webview-enter', 'environment-reused', 'environment-api-start', 'environment-api-return',
  'environment-callback', 'environment-wait-return', 'controller-api-start', 'controller-api-return',
  'controller-callback', 'controller-wait-return'] as const;
type Stage = typeof stages[number];
type Event = { seq: number; stage: Stage; elapsedUs: number };
function object(x: unknown): x is Record<string, unknown> { return !!x && typeof x === 'object' && !Array.isArray(x); }
function exact(x: Record<string, unknown>, keys: string[]) { return Object.keys(x).length === keys.length && keys.every(k => Object.hasOwn(x, k)); }
function integer(x: unknown): x is number { return typeof x === 'number' && Number.isSafeInteger(x) && x >= 0; }
export function classifyWryStartup(input: unknown) {
  const incomplete = { schema: 1, purpose: 'diagnostic-hints-only', phase: 'diagnostic-incomplete', events: [] as Event[], windowEndUs: null, pumps: null, pumpHint: null };
  if (!object(input) || !exact(input, ['schema', 'variant', 'purpose', 'events', 'attempted', 'recorded', 'dropped', 'sealed', 'inFlight', 'windowEndUs', 'pumps']) ||
    input.schema !== 1 || input.variant !== 'wry-webview2-stage-2' || input.purpose !== 'diagnostic-hints-only' ||
    !integer(input.windowEndUs) || input.sealed !== true || input.inFlight !== 0 || input.dropped !== 0 ||
    !Array.isArray(input.events) || input.events.length > 10 || input.recorded !== input.events.length || input.attempted !== input.events.length) return incomplete;
  const events: Event[] = []; const seen = new Set<Stage>(); let elapsed = 0;
  for (const row of input.events) {
    if (!object(row) || !exact(row, ['seq', 'stage', 'elapsedUs']) || row.seq !== events.length + 1 ||
      !integer(row.elapsedUs) || row.elapsedUs < elapsed) return incomplete;
    const stage = stages.find(s => s === row.stage);
    if (!stage || seen.has(stage) || (events.length === 0 && stage !== 'webview-enter') ||
      (stage !== 'webview-enter' && !seen.has('webview-enter'))) return incomplete;
    const environmentReady = seen.has('environment-reused') || seen.has('environment-wait-return');
    if ((stage === 'environment-reused' && seen.has('environment-api-start')) ||
      (stage === 'environment-api-start' && seen.has('environment-reused')) ||
      (['environment-api-return', 'environment-callback'].includes(stage) && !seen.has('environment-api-start')) ||
      (stage === 'environment-wait-return' && (!seen.has('environment-api-return') || !seen.has('environment-callback'))) ||
      (stage === 'controller-api-start' && !environmentReady) ||
      (['controller-api-return', 'controller-callback'].includes(stage) && !seen.has('controller-api-start')) ||
      (stage === 'controller-wait-return' && (!seen.has('controller-api-return') || !seen.has('controller-callback')))) return incomplete;
    seen.add(stage); events.push({ seq: events.length + 1, stage, elapsedUs: row.elapsedUs }); elapsed = row.elapsedUs;
  }
  if (input.windowEndUs < elapsed) return incomplete;
  const pumps = readPumps(input.pumps, input.windowEndUs); if (!pumps) return incomplete;
  for (const [scope, pump] of Object.entries(pumps)) {
    const api = events.find(e => e.stage === `${scope}-api-return`);
    const returned = events.find(e => e.stage === `${scope}-wait-return`);
    const callback = events.find(e => e.stage === `${scope}-callback`);
    if ((pump.entered && (!api || pump.lastUs < api.elapsedUs))
      || (returned && (pump.phase !== 'receiver-ready' || pump.lastUs > returned.elapsedUs))
      || (pump.phase === 'receiver-ready' && (!callback || pump.lastUs < callback.elapsedUs))) return incomplete;
  }
  if (seen.has('environment-reused') && pumps.environment.entered) return incomplete;
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
  return { schema: 1, purpose: 'diagnostic-hints-only', phase, events, windowEndUs: input.windowEndUs, pumps,
    pumpHint: phase === 'controller-callback-not-observed' ? controllerPumpHint(pumps.controller) : null };
}
import { readPumps, controllerPumpHint } from './wry-pump-state.ts';
