import { writeFileSync } from 'node:fs';
import path from 'node:path';
export type NativePhase = 'bootstrap' | 'session-create' | 'origin-check' | 'readiness' | 'main-screenshot' | 'unwired-contract' | 'keyboard' | 'diagnostics-screenshot' | 'result-write' | 'complete';
const operations = ['create-session', 'execute-sync', 'execute-async', 'find-element', 'click-element', 'keys', 'screenshot', 'other'] as const;
const outcomes = ['started', 'ok', 'timeout', 'transport', 'http', 'webdriver', 'invalid-response'] as const;
const protocolErrors = new Set(['session not created', 'timeout', 'invalid session id', 'javascript error', 'unknown error', 'no such element', 'stale element reference', 'invalid argument', 'no such window']);
export function normalizeDriverError(value: unknown): string { return typeof value === 'string' && protocolErrors.has(value) ? value : 'other'; }
export interface DriverTrace { operation: typeof operations[number]; outcome: typeof outcomes[number]; elapsedMs: number; status?: number; driverError?: string }
const localCodes = new Set(['NATIVE_ENV_NOT_CONFIGURED', 'NATIVE_ORIGIN_MISMATCH', 'NATIVE_BASELINE_DOCUMENT_REJECTED', 'NATIVE_DOCUMENT_NOT_READY', 'NATIVE_ZERO_OR_INCOMPLETE_CHECKS', 'NATIVE_KEYBOARD_INCOMPLETE', 'NATIVE_REFRESH_ARM_FAILED', 'NATIVE_REFRESH_NOT_OBSERVED', 'NATIVE_DRIVER_INVALID_ENVELOPE', 'NATIVE_DRIVER_REQUEST_FAILED', 'NATIVE_DRIVER_TIMEOUT', 'NATIVE_DRIVER_TRANSPORT_FAILED', 'NATIVE_DRIVER_INVALID_SESSION', 'NATIVE_SCREENSHOT_INVALID', 'NATIVE_SCREENSHOT_NOT_PNG', 'NATIVE_TEST_DEADLINE', 'NATIVE_EVIDENCE_WRITE_FAILED']);
export function createNativeObservation(output: string) {
  const started = performance.now();
  const state = { schema: 1, status: 'running', phase: 'bootstrap' as NativePhase, elapsedMs: 0,
    documentObservations: 0, documentStates: [] as string[], errorCode: '', originClass: 'unobserved', titleClass: 'unobserved', droppedRequests: 0,
    phases: [] as { phase: NativePhase; elapsedMs: number }[], requests: [] as DriverTrace[], lastRequest: null as DriverTrace | null };
  const save = () => {
    state.elapsedMs = Math.round(performance.now() - started);
    try { writeFileSync(path.join(output, 'native-observation.json'), JSON.stringify(state)); }
    catch { throw new Error('NATIVE_EVIDENCE_WRITE_FAILED'); }
  };
  save();
  return {
    phase(value: NativePhase) {
      state.phase = value;
      if (state.phases.length < 32) state.phases.push({ phase: value, elapsedMs: Math.round(performance.now() - started) });
      save();
    },
    request(value: DriverTrace) {
      const record: DriverTrace = {
        operation: operations.includes(value.operation) ? value.operation : 'other',
        outcome: outcomes.includes(value.outcome) ? value.outcome : 'invalid-response',
        elapsedMs: Number.isFinite(value.elapsedMs) ? Math.max(0, Math.round(value.elapsedMs)) : 0,
        ...(typeof value.status === 'number' && Number.isInteger(value.status) && value.status >= 100 && value.status <= 599 ? { status: value.status } : {}),
        ...(value.driverError ? { driverError: normalizeDriverError(value.driverError) } : {})
      };
      state.lastRequest = record;
      if (state.requests.length < 256) state.requests.push(record); else state.droppedRequests += 1;
      save();
    },
    origin(value: unknown) { state.originClass = value === 'http://tauri.localhost' ? 'bundled' : value === 'null' || value === 'about:blank' || value === '' ? 'initial' : 'other'; save(); },
    documentState(value: string) {
      const safe = ['initial', 'bundled-loading', 'bundled-ready', 'rejected'].includes(value) ? value : 'rejected';
      state.documentObservations += 1;
      if (state.documentStates.at(-1) !== safe && state.documentStates.length < 16) state.documentStates.push(safe);
      save();
    },
    title(value: unknown) { state.titleClass = value === '状態を確認中' ? 'checking' : value === 'バックエンド未接続' ? 'unwired' : value === '状態を取得できません' ? 'unavailable' : value === null || value === undefined ? 'absent' : 'other'; save(); },
    fail(error: unknown) {
      state.status = 'failed';
      state.errorCode = error instanceof Error && localCodes.has(error.message) ? error.message : error instanceof Error && error.name === 'AssertionError' ? 'NATIVE_ASSERTION_FAILED' : 'NATIVE_HARNESS_FAILED';
      save();
    },
    pass() { state.status = 'webview-passed-awaiting-close'; state.phase = 'complete'; save(); }
  };
}
