import type { UiKey } from './keyboard.ts';
import { normalizeDriverError, type DriverTrace } from './observation.ts';
function record(value: unknown): value is Record<string, unknown> { return typeof value === 'object' && value !== null && !Array.isArray(value); }
export class Driver {
  session = '';
  private readonly base: string;
  private readonly transport: typeof fetch;
  private readonly trace: ((value: DriverTrace) => void) | undefined;
  constructor(base: string, transport: typeof fetch = fetch, trace?: (value: DriverTrace) => void) {
    this.base = base;
    this.transport = transport;
    this.trace = trace;
    if (base !== 'http://127.0.0.1:4444') throw new Error('NATIVE_DRIVER_ORIGIN_REJECTED');
  }
  async request(method: string, route: string, body?: unknown): Promise<unknown> {
    const operation: DriverTrace['operation'] = route === '/session' ? 'create-session' : route.endsWith('/execute/sync') ? 'execute-sync' : route.endsWith('/execute/async') ? 'execute-async' : route.endsWith('/element') ? 'find-element' : route.endsWith('/click') ? 'click-element' : route.endsWith('/actions') ? 'keys' : route.endsWith('/screenshot') ? 'screenshot' : 'other';
    const started = performance.now();
    const emit = (outcome: DriverTrace['outcome'], status?: number, driverError?: string) => this.trace?.({ operation, outcome, elapsedMs: Math.round(performance.now() - started), status, driverError });
    emit('started');
    let response: Response;
    try {
      response = await this.transport(this.base + route, { method, headers: { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(15000) });
    } catch (error) {
      const timeout = error instanceof Error && (error.name === 'TimeoutError' || error.name === 'AbortError');
      emit(timeout ? 'timeout' : 'transport');
      throw new Error(timeout ? 'NATIVE_DRIVER_TIMEOUT' : 'NATIVE_DRIVER_TRANSPORT_FAILED', { cause: error });
    }
    let envelope: unknown;
    try { envelope = await response.json(); }
    catch { emit('invalid-response', response.status); throw new Error('NATIVE_DRIVER_INVALID_ENVELOPE'); }
    if (!record(envelope)) { emit('invalid-response', response.status); throw new Error('NATIVE_DRIVER_INVALID_ENVELOPE'); }
    const value = envelope.value;
    if (!response.ok || (record(value) && typeof value.error === 'string')) {
      emit(response.ok ? 'webdriver' : 'http', response.status, record(value) ? normalizeDriverError(value.error) : 'other');
      throw new Error('NATIVE_DRIVER_REQUEST_FAILED');
    }
    emit('ok', response.status);
    return value;
  }
  async create(application: string, profile: string): Promise<string> {
    const value = await this.request('POST', '/session', { capabilities: { alwaysMatch: { 'tauri:options': { application, webviewOptions: { userDataFolder: profile } } } } });
    if (!record(value) || typeof value.sessionId !== 'string' || !record(value.capabilities) || typeof value.capabilities.browserVersion !== 'string') throw new Error('NATIVE_DRIVER_INVALID_SESSION');
    this.session = encodeURIComponent(value.sessionId);
    return value.capabilities.browserVersion;
  }
  execute(script: string): Promise<unknown> { return this.request('POST', `/session/${this.session}/execute/sync`, { script, args: [] }); }
  invoke(): Promise<unknown> {
    return this.request('POST', `/session/${this.session}/execute/async`, { script: `const done = arguments[arguments.length - 1];
      const invoke = window.__TAURI__?.core?.invoke;
      if (typeof invoke !== 'function') { done({error:'BRIDGE_MISSING'}); return; }
      invoke('get_shell_status', {request:{protocolVersion:1}}).then(done, () => done({error:'IPC_ERROR'}));`, args: [] });
  }
  async click(selector: string): Promise<void> {
    const value = await this.request('POST', `/session/${this.session}/element`, { using: 'css selector', value: selector });
    const key = 'element-6066-11e4-a52e-4f735466cecf';
    if (!record(value) || typeof value[key] !== 'string') throw new Error('NATIVE_DRIVER_ELEMENT_MISSING');
    await this.request('POST', `/session/${this.session}/element/${encodeURIComponent(value[key])}/click`, {});
  }
  async press(key: UiKey): Promise<void> {
    const value = key === 'Shift+Tab' ? '\uE004' : { Tab: '\uE004', Enter: '\uE007', Space: '\uE00D' }[key];
    const actions = [{ type: 'keyDown', value }, { type: 'keyUp', value }];
    if (key === 'Shift+Tab') { actions.unshift({ type: 'keyDown', value: '\uE008' }); actions.push({ type: 'keyUp', value: '\uE008' }); }
    await this.request('POST', `/session/${this.session}/actions`, { actions: [{ type: 'key', id: 'ui-keyboard', actions }] });
  }
  async screenshot(): Promise<Buffer> {
    const value = await this.request('GET', `/session/${this.session}/screenshot`);
    if (typeof value !== 'string' || value.length > 14000000) throw new Error('NATIVE_SCREENSHOT_INVALID');
    const bytes = Buffer.from(value, 'base64');
    if (!bytes.subarray(0, 8).equals(Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]))) throw new Error('NATIVE_SCREENSHOT_NOT_PNG');
    return bytes;
  }
}
