import type { Observation, OperatorAdapter } from './model.ts';

// Explicit UI-only driver: no fetch, native invoke, timer, storage or credentials.
export function createMockAdapter() {
  let flow = 0;
  const listeners = new Set<(value: unknown) => void>();
  const adapter: OperatorAdapter = {
    mode: 'mock',
    requestApproval(value) { flow = value; return Promise.resolve(); },
    requestStop(value) {
      return value === flow ? Promise.resolve() : Promise.reject(new Error('DEMO_FLOW_MISMATCH'));
    },
    queryStatus(value) {
      return value === flow ? Promise.resolve() : Promise.reject(new Error('DEMO_FLOW_MISMATCH'));
    },
    subscribe(listener) { listeners.add(listener); return () => { listeners.delete(listener); }; }
  };
  return {
    adapter,
    emit(event: Observation) {
      if (flow > 0) for (const listener of listeners) listener(Object.freeze({ flow, event }));
    }
  };
}
