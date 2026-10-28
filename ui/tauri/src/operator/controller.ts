import { initialState, type OperatorAdapter, type OperatorState, type SampleId } from './model.ts';
export function createOperatorController({ adapter: _adapter, onChange }: {
  adapter: OperatorAdapter; onChange: (state: OperatorState) => void;
}) {
  let state = initialState();
  return {
    acceptSample(_sample: SampleId) { onChange(state); },
    snapshot() { return state; }
  };
}
