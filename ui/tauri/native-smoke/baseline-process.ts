import { spawn } from 'node:child_process';
export type ProcessOutcome = 'passed' | 'failed' | 'timeout' | 'error';

// 期限超過時は自分でspawnした直接childだけに終了を要求する。孫の終了は未確認。
export async function executeOwnedProcess(executable: string, args: string[], timeoutMs: number): Promise<ProcessOutcome> {
  return await new Promise(resolve => {
    let settled = false;
    const child = spawn(executable, args, { stdio: 'ignore', windowsHide: true });
    const finish = (value: ProcessOutcome) => { if (!settled) { settled = true; clearTimeout(timer); resolve(value); } };
    const timer = setTimeout(() => { try { child.kill(); } catch { /* 直接child以外は触らない。 */ } finish('timeout'); }, timeoutMs);
    child.once('error', () => finish('error'));
    child.once('exit', code => finish(code === 0 ? 'passed' : 'failed'));
  });
}
