import { useState } from 'react';
import { Copy, Send, Square } from 'lucide-react';
import { Button } from '../components/ui/button.tsx';
import { Card } from '../components/ui/card.tsx';
import { preview, type OperatorState } from './model.ts';

export function ConnectionPanel({ state, onApproval, onStop, onRetry, copy }: {
  state: OperatorState; onApproval: (confirmed: boolean) => void; onStop: () => void;
  onRetry: () => void;
  copy: (text: string) => Promise<void>;
}) {
  const [confirmed, setConfirmed] = useState(false);
  const [copyStatus, setCopyStatus] = useState({ phase: state.phase, message: '' });
  const canStop = state.validSession && !state.stopRequested && ['ready', 'connected', 'unconfirmed'].includes(state.phase);
  const canAct = (state.phase === 'review' && confirmed) || canStop;
  const canCopy = state.reviewed && state.validSession && !state.stopRequested && ['ready', 'connected'].includes(state.phase);
  const busy = ['waiting', 'connecting', 'stopping'].includes(state.phase);
  const action = state.phase === 'review' ? '承認を依頼（サンプル）' : canStop ? '接続を停止（サンプル）'
    : state.phase === 'stopping' ? '終了確認の応答を待っています' : busy ? '接続完了までお待ちください' : 'この体験の結果を表示中';
  return <Card className="operator-connection">
    <div><p className="operator-step-label">接続先と許可内容 / すべてサンプル</p><h2 id="review-heading" tabIndex={-1}>{canCopy ? '外部エージェントへ接続情報を渡す' : '接続先と許可内容を確認'}</h2></div>
    <dl className="operator-permissions">
      <dt>接続先</dt><dd>{preview.destination}</dd><dt>操作する人</dt><dd>{preview.operator}</dd>
      <dt>許可範囲</dt><dd>{preview.scope}</dd><dt>期限</dt><dd>{preview.deadline}</dd>
      <dt>接続情報</dt><dd>{preview.address}</dd><dt>認証情報</dt><dd>秘密鍵・パスワードは含みません</dd>
      <dt>通信の終了</dt><dd>{state.termination === 'confirmed' ? 'モックの確認応答あり' : state.termination === 'not_started' ? '依頼前・実通信なし' : '確認応答なし（終了未確認）'}</dd>
    </dl>
    {state.phase === 'review' && <label className="operator-check"><input type="checkbox" checked={confirmed} onChange={event => { setConfirmed(event.target.checked); }} />
      <span>接続先・許可範囲・期限を確認しました（サンプル）</span></label>}
    <div className="operator-action-area"><Button id="operator-action" variant={canStop ? 'danger' : 'default'}
      aria-disabled={!canAct || undefined} aria-busy={busy || undefined} aria-describedby="operator-action-help"
      onClick={() => { if (state.phase === 'review' && confirmed) onApproval(confirmed); else if (canStop) onStop(); }}>
      {canStop || state.phase === 'stopping' ? <Square className="icon" aria-hidden="true" focusable="false" /> : <Send className="icon" aria-hidden="true" focusable="false" />}{action}</Button>
      <p id="operator-action-help" className="caption">依頼や停止ボタンの受付だけでは、接続・終了の確認へ進みません。表示上の期限は権限を与えません。</p></div>
    <Button aria-disabled={!canCopy || undefined} aria-describedby="copy-help" onClick={() => { if (!canCopy) return; void (async () => {
      try { await copy(`操作プロトタイプの架空データ（実接続なし）\n${Object.values(preview).join('\n')}`); setCopyStatus({ phase: state.phase, message: 'サンプル接続情報をコピーしました。コード・秘密鍵・パスワードは含みません。' }); }
      catch { setCopyStatus({ phase: state.phase, message: 'コピーできませんでした。上のサンプル情報を選択してコピーできます。' }); }
    })(); }}><Copy className="icon" aria-hidden="true" focusable="false" />接続情報をコピー（サンプル）</Button>
    <p id="copy-help" className="caption">接続先と許可を確認し、モックの準備完了後にコピーできます。外部エージェントは起動しません。</p>
    <p className="caption" role="status" aria-live="polite">{copyStatus.phase === state.phase ? copyStatus.message : ''}</p>
    {state.phase === 'unconfirmed' && <div className="operator-retry"><Button variant="quiet" aria-disabled={!state.validSession || state.checkingStatus || undefined} aria-busy={state.checkingStatus || undefined} onClick={onRetry}>同じ接続の状態を再確認（モック）</Button>
      <p className="caption">同じ有効な接続の状態だけを確認します。接続し直したり、コマンドを送ったりしません。</p></div>}
  </Card>;
}
