import { initialState, type OperatorAdapter, type OperatorState, type SampleId } from './model.ts';
export function createOperatorController({ adapter: _adapter, onChange }: {
  adapter: OperatorAdapter; onChange: (state: OperatorState) => void;
}) {
  let state = initialState();
  return {
    acceptSample(sample: SampleId) {
      if (state.phase !== 'entry') return;
      state = { phase: 'review', sample, termination: 'not_started', diagnostic: 'DEMO_REVIEW' };
      onChange(state);
    },
    requestApproval(_confirmed: boolean) { return Promise.resolve(); },
    snapshot() { return state; }
  };
}
