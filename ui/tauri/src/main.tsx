import { createRoot } from 'react-dom/client';
import { App } from './App.tsx';
import type { ShellInvoke } from './controller.ts';
import '../design/tokens.css';
import '../design/components.css';
import './app.css';

declare global { interface Window { __TAURI__?: { core?: { invoke?: ShellInvoke } } } }
// The sole native IO adapter. UI and controller receive this dependency explicitly.
const nativeInvoke = window.__TAURI__?.core?.invoke;
const invoke: ShellInvoke | undefined = typeof nativeInvoke === 'function'
  ? (command, args) => nativeInvoke(command, args) : undefined;
const root = document.getElementById('root');
if (!root) throw new Error('Missing application root');
createRoot(root).render(<App invoke={invoke} />);
