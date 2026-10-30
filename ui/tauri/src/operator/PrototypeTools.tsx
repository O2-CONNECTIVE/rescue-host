import { X } from 'lucide-react';
import { Button } from '../components/ui/button.tsx';
import { Card } from '../components/ui/card.tsx';
import type { Observation, OperatorState, Phase } from './model.ts';
import { statusFor } from './status.ts';

const responses: Partial<Record<Phase, readonly (readonly [Observation, string])[]>> = {
  waiting: [['approved', '承認応答を再現'], ['denied', '承認拒否を再現'], ['expired', '期限切れを再現']],
  connecting: [['prepared', '接続情報の準備完了を再現'], ['failed', '接続失敗を再現'], ['expired', '期限切れを再現']],
  ready: [['connected', '外部エージェントの接続を再現'], ['expired', '期限切れを再現'], ['lost', '応答喪失を再現']],
  connected: [['expired', '期限切れを再現'], ['lost', '応答喪失を再現']],
  stopping: [['stop_confirmed', '終了確認の応答を再現'], ['lost', '停止未確認を再現']],
  unconfirmed: [['stop_confirmed', '終了確認の応答を再現'], ['expired', '期限切れを再現']],
  expired: [['stop_confirmed', '終了確認の応答を再現']],
  failed: [['stop_confirmed', '終了確認の応答を再現']]
};
export function PrototypeTools({ state, emit, closeWindow }: {
  state: OperatorState; emit: (event: Observation) => void; closeWindow: () => void;
}) {
  const choices = state.checkingStatus ? [['status_active', '同じ接続の稼働状態を再現'], ['stop_confirmed', '終了確認の応答を再現'], ['lost', '再確認の応答なしを再現']] as const
    : state.phase === 'failed' && state.termination === 'not_started' ? [] : responses[state.phase] ?? [];
  return <aside className="operator-tools" aria-label="プロトタイプの操作と診断">
    <Card className="operator-simulator"><h2>モックの応答</h2>
      <p className="caption">この欄で受信イベントを再現します。実際の承認や接続ではありません。</p>
      {choices.length > 0 && <div className="operator-response-buttons">{choices.map(([event, label]) => <Button key={event} variant="quiet" onClick={() => { emit(event); }}>{label}</Button>)}</div>}
      {!choices.length && <p className="caption">接続フローを進めると、再現できる応答が表示されます。</p>}
      <Button variant="quiet" onClick={closeWindow}><X className="icon" aria-hidden="true" focusable="false" />閉じる（トレイのモック）</Button>
    </Card>
    <Card role="region" aria-label="重要な通知（モック）"><h2>重要な通知（モック）</h2><p>{state.notice ? ({ connecting: '承認応答が届きました。', ready: '接続情報の準備が完了しました。', connected: '外部エージェントの接続を観測しました。', stopped: '終了確認の応答が届きました。', expired: '期限切れです。終了確認は別に必要です。', failed: statusFor(state).title, unconfirmed: '通信の終了を確認できません。' } as Partial<Record<Phase, string>>)[state.notice] : '重要な変化があると、ここに表示します。'}</p>
      <p className="caption">OS通知は未配線です。状態確認の要求や毎秒の時刻は通知しません。</p></Card>
    <Card className="operator-diagnostics"><h2>診断情報</h2><div className="diagnostics"><p className="caption">表示の根拠はモックからの観測です。入力した情報や実環境のログは含みません。</p>
        <dl className="operator-diagnostic-values"><dt>最新の固定 code</dt><dd><code>{state.diagnostic}</code></dd><dt>実通信</dt><dd>一度も開始していません</dd>
          <dt>終了確認</dt><dd>{state.termination === 'confirmed' ? 'モック応答のみ・実終了は未検証' : '終了確認の証拠なし'}</dd><dt>入力の保存</dt><dd>保存しません</dd></dl>
      </div></Card>
  </aside>;
}
