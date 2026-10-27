const timeout = Symbol('timeout');
const closed = Symbol('closed');

function acceptsStatus(status: unknown) {
  return status !== null && typeof status === 'object' && !Array.isArray(status)
    && Object.keys(status).sort().join(',') === 'canStart,canStop,integration,protocolVersion,termination'
    && 'protocolVersion' in status && 'integration' in status && 'canStart' in status && 'canStop' in status && 'termination' in status
    && status.protocolVersion === 1 && status.integration === 'not_connected'
    && status.canStart === false && status.canStop === false && status.termination === 'unconfirmed';
}

export type ShellState =
  | { kind: 'checking'; diagnostic: 'CHECKING' }
  | { kind: 'not_connected'; diagnostic: 'SHELL_UNWIRED' }
  | { kind: 'unavailable'; diagnostic: 'BRIDGE_MISSING' | 'IPC_TIMEOUT' | 'INVALID_RESPONSE' | 'IPC_ERROR' };
export type ShellInvoke = (command: 'get_shell_status', args: { request: { protocolVersion: 1 } }) => Promise<unknown>;
interface Options { invoke: ShellInvoke | undefined; onChange: (state: ShellState) => void; timeoutMs?: number }

export function createShellController({ invoke, onChange, timeoutMs = 2000 }: Options) {
  let disposed = false;
  let pending = false;
  let cancelPending: (() => void) | undefined;
  return {
    async refresh() {
      if (disposed || pending) return;
      onChange({ kind: 'checking', diagnostic: 'CHECKING' });
      if (typeof invoke !== 'function') {
        onChange({ kind: 'unavailable', diagnostic: 'BRIDGE_MISSING' });
        return;
      }
      pending = true;
      let timer: ReturnType<typeof setTimeout> | undefined;
      try {
        const status = await Promise.race([
          invoke('get_shell_status', { request: { protocolVersion: 1 } }),
          new Promise(resolve => { timer = setTimeout(() => resolve(timeout), timeoutMs); }),
          new Promise(resolve => { cancelPending = () => resolve(closed); })
        ]);
        if (disposed || status === closed) return;
        if (status === timeout) {
          onChange({ kind: 'unavailable', diagnostic: 'IPC_TIMEOUT' });
        } else {
          onChange(acceptsStatus(status)
            ? { kind: 'not_connected', diagnostic: 'SHELL_UNWIRED' }
            : { kind: 'unavailable', diagnostic: 'INVALID_RESPONSE' });
        }
      } catch {
        if (!disposed) onChange({ kind: 'unavailable', diagnostic: 'IPC_ERROR' });
      } finally {
        clearTimeout(timer);
        pending = false;
        cancelPending = undefined;
      }
    },
    dispose() {
      disposed = true;
      cancelPending?.();
    }
  };
}
