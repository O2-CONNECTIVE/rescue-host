import type { Pump } from './wry-pump-state.ts';
const counts = ['calls', 'apiReturns', 'callbacks', 'sends', 'returns'] as const;
const times = ['startedUs', 'apiStartUs', 'apiReturnUs', 'callbackUs', 'sentUs', 'returnUs'] as const;
type Scripts = Record<typeof counts[number], number> & Record<typeof times[number], number | null> &
  { result: 'idle' | 'pending' | 'ok' | 'error'; callbackOk: boolean | null; invalid: false };
export function readScripts(value: unknown, end: number, pump: Pump): Scripts | null {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
  const x = value as Record<string, unknown>; const keys = [...counts, ...times, 'result', 'callbackOk', 'invalid'];
  if (Object.keys(x).length !== keys.length || !keys.every(k => Object.hasOwn(x, k)) || x.invalid !== false
    || !counts.every(k => typeof x[k] === 'number' && Number.isSafeInteger(x[k]) && x[k] >= 0 && x[k] <= 128)
    || !times.every(k => x[k] === null || (typeof x[k] === 'number' && Number.isSafeInteger(x[k]) && x[k] >= 0 && x[k] <= end))
    || !['idle', 'pending', 'ok', 'error'].includes(String(x.result))
    || !(x.callbackOk === null || typeof x.callbackOk === 'boolean')) return null;
  const s = x as Scripts;
  if (s.calls === 0) return counts.every(k => s[k] === 0) && times.every(k => s[k] === null)
    && s.result === 'idle' && s.callbackOk === null && !pump.entered ? s : null;
  const prior = s.calls - 1;
  if (s.startedUs === null || s.result === 'idle' || !counts.slice(1).every(k => s[k] === prior || s[k] === s.calls)
    || s.sends > s.callbacks || (s.returns === prior) !== (s.result === 'pending')) return null;
  const pairs = [['apiReturnUs', 'apiReturns'], ['callbackUs', 'callbacks'], ['sentUs', 'sends'], ['returnUs', 'returns']] as const;
  if (pairs.some(([time, count]) => (s[time] !== null) !== (s[count] === s.calls))) return null;
  if ((s.callbackUs === null) !== (s.callbackOk === null)) return null;
  function ordered(first: number | null, second: number | null) {
    return second === null || (first !== null && first <= second);
  }
  if (!times.slice(1).every(k => ordered(s.startedUs, s[k]))
    || !ordered(s.apiStartUs, s.apiReturnUs) || !ordered(s.apiStartUs, s.callbackUs)
    || !ordered(s.callbackUs, s.sentUs) || !ordered(s.apiReturnUs, s.returnUs)
    || (s.result === 'ok' && (s.sends !== s.calls || s.callbackOk !== true))) return null;
  if (pump.entered && (s.apiReturnUs === null || pump.lastUs < s.apiReturnUs)) return null;
  if (pump.phase === 'receiver-ready' && (s.sentUs === null || pump.lastUs < s.sentUs)) return null;
  if (s.returnUs !== null && pump.entered && pump.lastUs > s.returnUs) return null;
  if (s.result === 'ok' && pump.phase !== 'receiver-ready') return null;
  return s;
}
export function scriptPhase(s: Scripts): string {
  if (s.calls === 0) return 'script-entry-not-observed';
  if (s.result === 'error') return 'script-error-return-observed';
  if (s.result === 'ok') return 'script-registrations-returned';
  if (s.apiStartUs === null) return 'script-api-start-not-observed';
  if (s.apiReturnUs === null) return 'script-api-return-not-observed';
  if (s.callbackUs === null) return 'script-callback-not-observed';
  if (s.sentUs === null) return 'script-callback-observed-send-not-observed';
  return 'script-send-observed-wait-return-not-observed';
}
