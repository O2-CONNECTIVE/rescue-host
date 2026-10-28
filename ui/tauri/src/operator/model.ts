export type SampleId = 'code' | 'definition';
export type Phase = 'entry' | 'review' | 'waiting' | 'connecting' | 'ready' | 'connected' | 'stopping' | 'stopped' | 'expired' | 'failed' | 'unconfirmed';
export type Observation = 'approved' | 'denied' | 'prepared' | 'connected' | 'stop_confirmed' | 'expired' | 'failed' | 'lost' | 'status_active';
export type Termination = 'not_started' | 'unconfirmed' | 'confirmed';
export interface OperatorState {
  readonly phase: Phase;
  readonly sample: SampleId | undefined;
  readonly termination: Termination;
  readonly diagnostic: string;
  readonly validSession: boolean;
  readonly stopRequested: boolean;
  readonly checkingStatus: boolean;
  readonly reviewed: boolean;
  readonly visibility: 'window' | 'tray';
  readonly notice: Phase | undefined;
}
export interface OperatorAdapter {
  readonly mode: 'mock';
  requestApproval(flow: number): Promise<void>;
  requestStop(flow: number): Promise<void>;
  queryStatus(flow: number): Promise<void>;
  subscribe(listener: (observation: unknown) => void): () => void;
}
export const samples: Record<SampleId, string> = {
  code: 'DEMO-2486', definition: '{"demo":"rescue-client-v1"}'
};
export function readSample(kind: SampleId, raw: string): SampleId | undefined {
  return raw.length <= 128 && raw.trim() === samples[kind] ? kind : undefined;
}
export const preview = {
  destination: '練習用PC 01（架空）', operator: 'サポート担当（あなた・サンプル）',
  scope: '指定した1台への SSH 接続・非管理者ユーザー',
  deadline: '承認後15分（サンプル・自動延長なし）',
  address: 'demo-host.invalid / port 2222 / user support-demo（架空）'
};
export function initialState(): OperatorState {
  return Object.freeze({ phase: 'entry', sample: undefined, termination: 'not_started', diagnostic: 'DEMO_READY',
    validSession: false, stopRequested: false, checkingStatus: false, reviewed: false, visibility: 'window', notice: undefined });
}
export function readObservation(value: unknown): { flow: number; event: Observation } | undefined {
  const names: readonly string[] = ['approved', 'denied', 'prepared', 'connected', 'stop_confirmed', 'expired', 'failed', 'lost', 'status_active'];
  if (typeof value !== 'object' || value === null || Array.isArray(value) || Object.keys(value).length !== 2
    || !('flow' in value) || typeof value.flow !== 'number' || !Number.isSafeInteger(value.flow) || value.flow < 1
    || !('event' in value) || typeof value.event !== 'string' || !names.includes(value.event)) return;
  return { flow: value.flow, event: value.event as Observation };
}
