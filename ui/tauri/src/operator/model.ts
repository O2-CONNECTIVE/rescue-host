export type SampleId = 'code' | 'definition';
export type Phase = 'entry' | 'review' | 'waiting' | 'connecting' | 'connected' | 'stopping' | 'stopped' | 'expired' | 'failed' | 'unconfirmed';
export type Observation = 'approved' | 'denied' | 'connected' | 'stop_confirmed' | 'expired' | 'failed' | 'lost';
export type Termination = 'not_started' | 'unconfirmed' | 'confirmed';
export interface OperatorState {
  readonly phase: Phase;
  readonly sample: SampleId | undefined;
  readonly termination: Termination;
  readonly diagnostic: string;
}
export interface OperatorAdapter {
  readonly mode: 'mock';
  requestApproval(flow: number): Promise<void>;
  requestStop(flow: number): Promise<void>;
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
  deadline: '承認後15分（サンプル・自動延長なし）'
};
export function initialState(): OperatorState {
  return { phase: 'entry', sample: undefined, termination: 'not_started', diagnostic: 'DEMO_READY' };
}
