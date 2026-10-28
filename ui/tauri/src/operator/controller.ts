import { initialState, readObservation, type OperatorAdapter, type OperatorState, type SampleId } from './model.ts';

// UI-only mock contract. No native command, raw code, authority or wire protocol.
export function createOperatorController({ adapter, onChange }: {
  adapter: OperatorAdapter; onChange: (state: OperatorState) => void;
}) {
  if (adapter.mode !== 'mock') throw new Error('MOCK_ADAPTER_REQUIRED');
  let state = initialState();
  let flow = 0;
  let disposed = false;
  function live() { return ['waiting', 'connecting', 'connected', 'stopping'].includes(state.phase); }
  function publish(next: Partial<OperatorState>) {
    state = Object.freeze({ ...state, ...next });
    onChange(state);
  }
  const unsubscribe = adapter.subscribe(value => {
    if (disposed || !live()) return;
    let observation;
    try { observation = readObservation(value); } catch { /* Fixed diagnostic only. */ }
    if (!observation) {
      publish({ phase: 'unconfirmed', diagnostic: 'DEMO_INVALID_OBSERVATION' });
      return;
    }
    if (observation.flow !== flow) return;
    switch (observation.event) {
      case 'approved':
        if (state.phase === 'waiting') publish({ phase: 'connecting', diagnostic: 'DEMO_APPROVED' });
        break;
      case 'connected':
        if (state.phase === 'connecting') publish({ phase: 'connected', diagnostic: 'DEMO_CONNECTED' });
        break;
      case 'stop_confirmed':
        if (state.phase === 'stopping') publish({ phase: 'stopped', termination: 'confirmed', diagnostic: 'DEMO_STOP_CONFIRMED' });
        break;
      case 'expired': publish({ phase: 'expired', diagnostic: 'DEMO_EXPIRED' }); break;
      case 'denied':
      case 'failed': publish({ phase: 'failed', diagnostic: 'DEMO_FAILED' }); break;
      case 'lost': publish({ phase: 'unconfirmed', diagnostic: 'DEMO_TERMINATION_UNCONFIRMED' }); break;
    }
  });
  async function request(operation: 'approval' | 'stop', confirmed = true) {
    if (disposed || !confirmed || state.phase !== (operation === 'approval' ? 'review' : 'connected')) return;
    const current = flow;
    publish({ phase: operation === 'approval' ? 'waiting' : 'stopping', termination: 'unconfirmed',
      diagnostic: operation === 'approval' ? 'DEMO_APPROVAL_REQUESTED' : 'DEMO_STOP_REQUESTED' });
    if (disposed || flow !== current) return;
    try {
      if (operation === 'approval') await adapter.requestApproval(current);
      else await adapter.requestStop(current);
    } catch {
      if (!disposed && flow === current && live()) publish({
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
    reset() {
      if (disposed) return;
      flow += 1;
      state = initialState();
      onChange(state);
    },
    dispose() { if (!disposed) { disposed = true; flow += 1; unsubscribe(); } },
    snapshot() { return state; }
  };
}
