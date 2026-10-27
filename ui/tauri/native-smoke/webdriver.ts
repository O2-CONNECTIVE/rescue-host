interface Envelope { value?: unknown }
function record(value: unknown): value is Record<string, unknown> { return typeof value === 'object' && value !== null && !Array.isArray(value); }
export class Driver {
  session = '';
  private readonly base: string;
  constructor(base: string) {
    this.base = base;
    if (base !== 'http://127.0.0.1:4444') throw new Error('NATIVE_DRIVER_ORIGIN_REJECTED');
  }
  async request(method: string, route: string, body?: unknown): Promise<unknown> {
    const response = await fetch(this.base + route, { method, headers: { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(15000) });
    const envelope: unknown = await response.json();
    if (!record(envelope)) throw new Error('NATIVE_DRIVER_INVALID_ENVELOPE');
    const value = (envelope as Envelope).value;
    if (!response.ok || (record(value) && typeof value.error === 'string')) throw new Error('NATIVE_DRIVER_REQUEST_FAILED');
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
  async screenshot(): Promise<Buffer> {
    const value = await this.request('GET', `/session/${this.session}/screenshot`);
    if (typeof value !== 'string' || value.length > 14000000) throw new Error('NATIVE_SCREENSHOT_INVALID');
    const bytes = Buffer.from(value, 'base64');
    if (!bytes.subarray(0, 8).equals(Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]))) throw new Error('NATIVE_SCREENSHOT_NOT_PNG');
    return bytes;
  }
}
