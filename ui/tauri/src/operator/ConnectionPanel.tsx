import { useState } from 'react';
import { Copy, Send, Square } from 'lucide-react';
import { Button } from '../components/ui/button.tsx';
import { Card } from '../components/ui/card.tsx';
import { preview, type OperatorState } from './model.ts';

export function ConnectionPanel({ state, onApproval, onStop, onRetry, onReset, copy }: {
  state: OperatorState; onApproval: (confirmed: boolean) => void; onStop: () => void;
  onRetry: () => void; onReset: () => void; copy: (text: string) => Promise<void>;
}) {
  const [confirmed, setConfirmed] = useState(false);
  const [copyStatus, setCopyStatus] = useState({ phase: state.phase, message: '' });
  const canStop = state.validSession && !state.stopRequested && ['ready', 'connected', 'unconfirmed'].includes(state.phase);
  const canCopy = state.reviewed && state.validSession && !state.stopRequested && ['ready', 'connected'].includes(state.phase);
  const busy = ['waiting', 'connecting', 'stopping'].includes(state.phase);
  const reviewing = state.phase === 'review';
  const terminal = ['stopped', 'expired', 'failed', 'unconfirmed'].includes(state.phase);
  const action = reviewing ? '承認を依頼（サンプル）' : state.phase === 'waiting' ? '承認の返事を待っています'
    : state.phase === 'connecting' ? '接続情報を準備しています' : '終了確認の応答を待っています';
  return <Card className="operator-connection">
    <div><p className="operator-step-label">接続先</p><h2 id="review-heading" tabIndex={-1}>{preview.destination}</h2></div>
    <dl className="operator-permissions"><dt>操作する人</dt><dd>{preview.operator}</dd>
      <dt>許可範囲</dt><dd>{preview.scope}</dd><dt>期限</dt><dd>{preview.deadline}</dd></dl>
    {reviewing && <label className="operator-check"><input type="checkbox" checked={confirmed} onChange={event => { setConfirmed(event.target.checked); }} />
      <span>接続先・許可範囲・期限を確認しました</span></label>}
    <div className="operator-action-area">
      {(reviewing || busy) && <Button id="operator-action" aria-disabled={!reviewing || !confirmed || undefined} aria-busy={busy || undefined} aria-describedby="operator-state-description"
        onClick={() => { if (reviewing && confirmed) onApproval(confirmed); }}>{state.phase === 'stopping' ? <Square className="icon" aria-hidden="true" focusable="false" /> : <Send className="icon" aria-hidden="true" focusable="false" />}{action}</Button>}
      {canCopy && <Button aria-describedby="operator-state-description" onClick={() => { void (async () => {
        try { await copy(`操作プロトタイプの架空データ（実接続なし）\n${Object.values(preview).join('\n')}`); setCopyStatus({ phase: state.phase, message: '接続情報をコピーしました（サンプル）。' }); }
        catch { setCopyStatus({ phase: state.phase, message: 'コピーできません。「詳細を見る」の接続情報を選択してコピーしてください。' }); }
      })(); }}><Copy className="icon" aria-hidden="true" focusable="false" />接続情報をコピー（サンプル）</Button>}
      {canStop && <Button variant="danger" onClick={onStop}><Square className="icon" aria-hidden="true" focusable="false" />接続を停止（サンプル）</Button>}
      {state.phase === 'unconfirmed' && <Button aria-disabled={!state.validSession || state.checkingStatus || undefined} aria-busy={state.checkingStatus || undefined}
        aria-describedby="operator-state-description" onClick={onRetry}>同じ接続の状態を再確認（モック）</Button>}
    </div>
    {copyStatus.phase === state.phase && copyStatus.message && <p className="caption" role="status" aria-live="polite">{copyStatus.message}</p>}
    {reviewing && <Button variant="quiet" onClick={onReset}>入力し直す</Button>}
    {terminal && <Button variant={state.termination === 'unconfirmed' ? 'quiet' : 'default'} aria-disabled={state.termination === 'unconfirmed' || undefined}
      aria-describedby="operator-state-description" onClick={onReset}>新しい接続の体験へ</Button>}
  </Card>;
}
