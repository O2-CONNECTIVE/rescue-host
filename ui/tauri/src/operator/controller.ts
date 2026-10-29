import { initialState, preview, readObservation, type OperatorAdapter, type OperatorState, type SampleId } from './model.ts';
import type { createMemoryHistory, HistoryResult } from './history.ts';

// UI-only mock contract. No native command, raw code, authority or wire protocol.
export function createOperatorController({ adapter, onChange, history }: {
  adapter: OperatorAdapter; onChange: (state: OperatorState) => void; history?: ReturnType<typeof createMemoryHistory>;
}) {
  if (adapter.mode !== 'mock') throw new Error('MOCK_ADAPTER_REQUIRED');
  let state = initialState();
  let flow = 0;
  let disposed = false;
  let updateHistory: ((result: HistoryResult) => void) | undefined;
  function live() { return ['waiting', 'connecting', 'ready', 'connected', 'stopping'].includes(state.phase); }
  function publish(next: Partial<OperatorState>) {
    const important = next.phase && next.phase !== state.phase && ['connecting', 'ready', 'connected', 'stopped', 'expired', 'failed', 'unconfirmed'].includes(next.phase);
    state = Object.freeze({ ...state, ...next, ...(important ? { notice: next.phase } : {}) });
    if (history && next.phase && ['stopped', 'unconfirmed', 'expired', 'failed'].includes(next.phase)) {
      const result: HistoryResult = next.phase === 'stopped' ? 'confirmed' : next.phase === 'expired' ? 'expired' : next.phase === 'failed' ? 'failed' : 'unconfirmed';
      if (updateHistory) updateHistory(result);
      else updateHistory = history.record(preview.destination, result);
    }
    onChange(state);
  }
  const unsubscribe = adapter.subscribe(value => {
    if (disposed || state.termination !== 'unconfirmed') return;
    let observation;
    try { observation = readObservation(value); } catch { /* Fixed diagnostic only. */ }
    if (!observation) {
      publish({ phase: 'unconfirmed', validSession: false, checkingStatus: false, diagnostic: 'DEMO_INVALID_OBSERVATION' });
      return;
    }
    if (observation.flow !== flow) return;
    switch (observation.event) {
      case 'approved':
        if (state.phase === 'waiting') publish({ phase: 'connecting', validSession: true, diagnostic: 'DEMO_APPROVED' });
        break;
      case 'prepared':
        if (state.phase === 'connecting') publish({ phase: 'ready', diagnostic: 'DEMO_PREPARED' });
        break;
      case 'connected':
        if (state.phase === 'ready') publish({ phase: 'connected', diagnostic: 'DEMO_CONNECTED' });
        break;
      case 'stop_confirmed':
        if (['stopping', 'unconfirmed', 'expired', 'failed'].includes(state.phase)) publish({ phase: 'stopped', termination: 'confirmed', validSession: false, checkingStatus: false, diagnostic: 'DEMO_STOP_CONFIRMED' });
        break;
      case 'expired':
        if (live() || state.phase === 'unconfirmed') publish({ phase: 'expired', validSession: false, checkingStatus: false, diagnostic: 'DEMO_EXPIRED' });
        break;
      case 'denied':
        if (state.phase === 'waiting') publish({ phase: 'failed', termination: 'not_started', validSession: false, diagnostic: 'DEMO_DENIED' });
        break;
      case 'failed':
        if (live()) publish({ phase: 'failed', checkingStatus: false,
          diagnostic: state.phase === 'connecting' ? 'DEMO_PREPARATION_FAILED' : 'DEMO_FAILED' });
        break;
      case 'lost':
        if (live() || state.checkingStatus) publish({ phase: 'unconfirmed', checkingStatus: false, diagnostic: 'DEMO_TERMINATION_UNCONFIRMED' });
        break;
      case 'status_active':
        if (state.checkingStatus && state.validSession && state.phase === 'unconfirmed') publish({
          phase: state.stopRequested ? 'unconfirmed' : 'connected', checkingStatus: false,
          diagnostic: state.stopRequested ? 'DEMO_STOP_STILL_UNCONFIRMED' : 'DEMO_STATUS_ACTIVE'
        });
        break;
    }
  });
  async function request(operation: 'approval' | 'stop', confirmed = true) {
    const canStop = !state.stopRequested && state.validSession && ['ready', 'connected', 'unconfirmed'].includes(state.phase);
    if (disposed || !confirmed || (operation === 'approval' ? state.phase !== 'review' : !canStop)) return;
    const current = flow;
    publish({ phase: operation === 'approval' ? 'waiting' : 'stopping', termination: 'unconfirmed',
      reviewed: true, stopRequested: operation === 'stop', checkingStatus: false,
      diagnostic: operation === 'approval' ? 'DEMO_APPROVAL_REQUESTED' : 'DEMO_STOP_REQUESTED' });
    if (disposed || flow !== current) return;
    try {
      if (operation === 'approval') await adapter.requestApproval(current);
      else await adapter.requestStop(current);
    } catch {
      // 観測で次の段階へ進んだ後は、古い要求の失敗で表示を巻き戻さない。
      if (!disposed && flow === current && state.phase === (operation === 'approval' ? 'waiting' : 'stopping')) publish({
        phase: operation === 'approval' ? 'failed' : 'unconfirmed',
        diagnostic: operation === 'approval' ? 'DEMO_REQUEST_FAILED' : 'DEMO_TERMINATION_UNCONFIRMED'
      });
    }
  }
  return {
    acceptSample(sample: SampleId) {
      if (disposed || state.phase !== 'entry' || (sample !== 'code' && sample !== 'definition')) return;
      flow += 1;
      publish({ phase: 'review', sample, termination: 'not_started', diagnostic: 'DEMO_REVIEW' });
    },
    requestApproval(confirmed: boolean) { return request('approval', confirmed); },
    requestStop() { return request('stop'); },
    async retryStatus() {
      if (disposed || !state.validSession || state.phase !== 'unconfirmed' || state.checkingStatus) return;
      const current = flow;
      publish({ checkingStatus: true, diagnostic: 'DEMO_STATUS_REQUESTED' });
      try { await adapter.queryStatus(current); }
      catch {
        if (!disposed && flow === current && state.checkingStatus) publish({ checkingStatus: false, diagnostic: 'DEMO_STATUS_UNAVAILABLE' });
      }
    },
    minimizeToTray() { if (!disposed) publish({ visibility: 'tray' }); },
    restoreWindow() { if (!disposed) publish({ visibility: 'window' }); },
    reset() {
      // History deletion, window closing and display reset cannot unlock this.
      if (disposed || state.termination === 'unconfirmed') return;
      flow += 1;
      state = initialState();
      updateHistory = undefined;
      onChange(state);
    },
    dispose() { if (!disposed) { disposed = true; flow += 1; unsubscribe(); } },
    snapshot() { return state; }
  };
}
