import { initialState, type OperatorAdapter, type OperatorState, type SampleId } from './model.ts';
export function createOperatorController({ adapter, onChange }: {
  adapter: OperatorAdapter; onChange: (state: OperatorState) => void;
}) {
  let state = initialState();
  let flow = 0;
  function publish(next: Partial<OperatorState>) {
    state = Object.freeze({ ...state, ...next });
    onChange(state);
  }
  adapter.subscribe(value => {
    if (typeof value !== 'object' || value === null || !('flow' in value) || value.flow !== flow || !('event' in value)) return;
    if (!['waiting', 'connecting', 'connected', 'stopping'].includes(state.phase)) return;
    if (value.event === 'approved' && state.phase === 'waiting') publish({ phase: 'connecting', diagnostic: 'DEMO_APPROVED' });
    if (value.event === 'connected' && state.phase === 'connecting') publish({ phase: 'connected', diagnostic: 'DEMO_CONNECTED' });
    if (value.event === 'stop_confirmed' && state.phase === 'stopping') publish({ phase: 'stopped', termination: 'confirmed', diagnostic: 'DEMO_STOP_CONFIRMED' });
    if (value.event === 'expired') publish({ phase: 'expired', diagnostic: 'DEMO_EXPIRED' });
    if (value.event === 'denied' || value.event === 'failed') publish({ phase: 'failed', diagnostic: 'DEMO_FAILED' });
    if (value.event === 'lost') publish({ phase: 'unconfirmed', diagnostic: 'DEMO_TERMINATION_UNCONFIRMED' });
  });
  return {
    acceptSample(sample: SampleId) {
      if (state.phase !== 'entry') return;
      flow += 1;
      publish({ phase: 'review', sample, termination: 'not_started', diagnostic: 'DEMO_REVIEW' });
    },
    async requestApproval(confirmed: boolean) {
      if (state.phase !== 'review' || !confirmed) return;
      publish({ phase: 'waiting', termination: 'unconfirmed', diagnostic: 'DEMO_APPROVAL_REQUESTED' });
      await adapter.requestApproval(flow);
    },
    async requestStop() {
      if (state.phase !== 'connected') return;
      publish({ phase: 'stopping', termination: 'unconfirmed', diagnostic: 'DEMO_STOP_REQUESTED' });
      await adapter.requestStop(flow);
    },
    reset() {},
    dispose() {},
    snapshot() { return state; }
  };
}
