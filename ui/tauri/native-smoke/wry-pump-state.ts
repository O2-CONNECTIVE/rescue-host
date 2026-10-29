const phases = ['not-entered', 'receiver-check', 'get-message', 'translate', 'dispatch', 'receiver-ready', 'quit', 'os-error'] as const;
type Phase = typeof phases[number];
export type Pump = { entered: boolean; phase: Phase; iterations: number; getCalls: number; getReturns: number;
  dispatchCalls: number; dispatchReturns: number; lastUs: number; invalid: false };
function object(x: unknown): x is Record<string, unknown> { return !!x && typeof x === 'object' && !Array.isArray(x); }
function read(input: unknown, end: number): Pump | null {
  const keys = ['entered', 'phase', 'iterations', 'getCalls', 'getReturns', 'dispatchCalls', 'dispatchReturns', 'lastUs', 'invalid'];
  if (!object(input) || Object.keys(input).length !== keys.length || !keys.every(k => Object.hasOwn(input, k))
    || typeof input.entered !== 'boolean' || input.invalid !== false) return null;
  const phase = phases.find(x => x === input.phase); if (!phase) return null;
  const counts = ['iterations', 'getCalls', 'getReturns', 'dispatchCalls', 'dispatchReturns'] as const;
  if (!counts.every(k => typeof input[k] === 'number' && Number.isSafeInteger(input[k]) && input[k] >= 0 && input[k] <= 1000000)
    || typeof input.lastUs !== 'number' || !Number.isSafeInteger(input.lastUs) || input.lastUs < 0 || input.lastUs > end) return null;
  const [iterations, getCalls, getReturns, dispatchCalls, dispatchReturns] = counts.map(k => Number(input[k]));
  if (!input.entered) {
    if (phase !== 'not-entered' || [iterations, getCalls, getReturns, dispatchCalls, dispatchReturns, input.lastUs].some(x => x !== 0)) return null;
  } else {
    if (phase === 'not-entered' || !(dispatchReturns <= dispatchCalls && dispatchCalls <= getReturns && getReturns <= getCalls && getCalls <= iterations)) return null;
    const balanced = getCalls === getReturns && getReturns === dispatchCalls && dispatchCalls === dispatchReturns;
    if ((phase === 'receiver-check' && !(balanced && (iterations === getCalls || iterations === getCalls + 1)))
      || (phase === 'receiver-ready' && !(balanced && iterations === getCalls + 1))
      || (phase === 'get-message' && !(getCalls === getReturns + 1 && getReturns === dispatchCalls && dispatchCalls === dispatchReturns && iterations === getCalls))
      || (['translate', 'quit', 'os-error'].includes(phase) && !(getCalls === getReturns && getReturns === dispatchCalls + 1 && dispatchCalls === dispatchReturns && iterations === getCalls))
      || (phase === 'dispatch' && !(getCalls === getReturns && getReturns === dispatchCalls && dispatchCalls === dispatchReturns + 1 && iterations === getCalls))) return null;
  }
  return { entered: input.entered, phase, iterations, getCalls, getReturns,
    dispatchCalls, dispatchReturns, lastUs: input.lastUs, invalid: false };
}
export function readPumps(input: unknown, end: number) {
  if (!object(input) || Object.keys(input).length !== 3 || !['environment', 'controller', 'script'].every(k => Object.hasOwn(input, k))) return null;
  const environment = read(input.environment, end); const controller = read(input.controller, end);
  const script = read(input.script, end);
  return environment && controller && script ? { environment, controller, script } : null;
}
export function controllerPumpHint(pump: Pump): string {
  if (!pump.entered) return 'controller-pump-entry-not-observed';
  return { 'not-entered': 'controller-pump-entry-not-observed', 'receiver-check': 'controller-receiver-check-last-observed',
    'get-message': 'controller-get-message-return-not-observed', translate: 'controller-translate-return-not-observed',
    dispatch: 'controller-dispatch-return-not-observed', 'receiver-ready': 'controller-receiver-ready',
    quit: 'controller-quit-observed', 'os-error': 'controller-get-message-error-observed' }[pump.phase];
}
