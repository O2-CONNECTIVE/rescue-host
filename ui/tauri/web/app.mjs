import { createShellController } from './controller.mjs';

const title = document.querySelector('#status-title');
const description = document.querySelector('#status-description');
const diagnostic = document.querySelector('#diagnostic');
const refresh = document.querySelector('#refresh');
const statusPanel = document.querySelector('.status');
const statusIcon = document.querySelector('#status-icon');
const messages = {
  checking: ['状態を確認中', '接続機能の状態を取得しています。'],
  not_connected: ['バックエンド未接続', '接続機能は未配線です。開始や停止は利用できず、通信の終了状態は未確認です。'],
  unavailable: ['状態を取得できません', '接続機能の状態を取得できませんでした。接続や通信終了は確認できません。']
};
const nativeInvoke = window.__TAURI__?.core?.invoke;
const controller = createShellController({
  invoke: typeof nativeInvoke === 'function' ? nativeInvoke : undefined,
  onChange(state) {
    const [heading, explanation] = messages[state.kind];
    title.textContent = heading;
    statusPanel.classList.toggle('status--unconfirmed', state.kind === 'unavailable');
    statusIcon.textContent = state.kind === 'unavailable' ? '!' : '○';
    description.textContent = state.diagnostic === 'BRIDGE_MISSING'
      ? 'ブラウザー表示では状態取得を利用できません。接続や通信終了は確認できません。'
      : explanation;
    diagnostic.textContent = state.diagnostic;
    refresh.disabled = state.kind === 'checking';
  }
});
refresh.addEventListener('click', () => { void controller.refresh(); });
window.addEventListener('pagehide', () => controller.dispose(), { once: true });
void controller.refresh();
