const stages = ['setup-enter', 'webview-build-start', 'webview-build-ok', 'webview-build-error', 'setup-return', 'first-page-load-finished'] as const;
type Stage = typeof stages[number];
type Event = { seq: number; stage: Stage; elapsedUs: number };
function object(value: unknown): value is Record<string, unknown> { return !!value && typeof value === 'object' && !Array.isArray(value); }
function exact(value: Record<string, unknown>, keys: string[]) { return Object.keys(value).length === keys.length && keys.every(key => Object.hasOwn(value, key)); }
function integer(value: unknown): value is number { return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0; }
export function classifyAppStartup(input: unknown) {
  const incomplete = { schema: 1, purpose: 'diagnostic-hints-only', phase: 'diagnostic-incomplete', events: [] as Event[] };
  if (!object(input) || !exact(input, ['schema', 'variant', 'purpose', 'events', 'attempted', 'recorded', 'dropped', 'sealed', 'inFlight']) ||
    input.schema !== 1 || input.variant !== 'host-startup-stage-1' || input.purpose !== 'diagnostic-hints-only' ||
    input.sealed !== true || input.inFlight !== 0 || input.dropped !== 0 || !Array.isArray(input.events) || input.events.length > 6 ||
    input.recorded !== input.events.length || input.attempted !== input.events.length) return incomplete;
  const events: Event[] = []; const seen = new Set<Stage>(); let elapsed = 0;
  for (const item of input.events) {
    if (!object(item) || !exact(item, ['seq', 'stage', 'elapsedUs']) || item.seq !== events.length + 1 ||
      typeof item.stage !== 'string' || !stages.some(stage => stage === item.stage) ||
      !integer(item.elapsedUs) || item.elapsedUs < elapsed) return incomplete;
    const stage = stages.find(stage => stage === item.stage);
    if (!stage || seen.has(stage)) return incomplete;
    if ((events.length === 0 && stage !== 'setup-enter') ||
      (stage === 'webview-build-start' && !seen.has('setup-enter')) ||
      (stage !== 'setup-enter' && stage !== 'webview-build-start' && !seen.has('webview-build-start')) ||
      (stage === 'setup-return' && !seen.has('webview-build-ok')) ||
      (stage === 'webview-build-error' && (seen.has('webview-build-ok') || seen.has('first-page-load-finished'))) ||
      ((stage === 'webview-build-ok' || stage === 'first-page-load-finished') && seen.has('webview-build-error'))) return incomplete;
    events.push({ seq: events.length + 1, stage, elapsedUs: item.elapsedUs }); seen.add(stage); elapsed = item.elapsedUs;
  }
  const phase = seen.has('first-page-load-finished') ? 'first-page-load-finished'
    : seen.has('webview-build-error') ? 'webview-build-error'
    : seen.has('webview-build-ok') ? 'webview-built-page-load-not-observed'
    : seen.has('webview-build-start') ? 'webview-build-not-observed'
    : seen.has('setup-enter') ? 'setup-entered-build-not-observed' : 'setup-not-observed';
  return { schema: 1, purpose: 'diagnostic-hints-only', phase, events };
}
