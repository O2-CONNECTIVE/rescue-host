import { ChevronRight } from 'lucide-react';
import { Button } from '../components/ui/button.tsx';
import { Card } from '../components/ui/card.tsx';
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '../components/ui/collapsible.tsx';
import { preview, type Observation, type OperatorState } from './model.ts';
import type { createMemoryHistory } from './history.ts';
import { status } from './status.ts';
import { PrototypeTools } from './PrototypeTools.tsx';
import { HistoryPanel } from './HistoryPanel.tsx';

export function OperatorDetails({ state, open, onOpenChange, emit, closeWindow, history, advanceClock }: {
  state: OperatorState; open: boolean; onOpenChange: (open: boolean) => void; emit: (event: Observation) => void;
  closeWindow: () => void; history: ReturnType<typeof createMemoryHistory>; advanceClock: () => void;
}) {
  return <Collapsible open={open} onOpenChange={onOpenChange} className="operator-details">
    <CollapsibleTrigger asChild><Button variant="quiet" className="disclosure"><ChevronRight className="icon" aria-hidden="true" focusable="false" />詳細を見る</Button></CollapsibleTrigger>
    <CollapsibleContent className="operator-details-content">
      <ol className="operator-steps" aria-label="接続の流れ">{['内容を確認', '承認を待つ', '接続・作業', '終了を確認'].map((label, index) => <li key={label} aria-current={status[state.phase].step === index ? 'step' : undefined}><span aria-hidden="true">{index + 1}</span>{label}</li>)}</ol>
      <Card><h2>接続の詳細</h2><dl className="operator-diagnostic-values"><dt>接続情報（架空）</dt><dd>{preview.address}</dd><dt>認証情報</dt><dd>秘密鍵・パスワードは含みません</dd>
        <dt>通信の終了</dt><dd>{state.termination === 'confirmed' ? 'モックの確認応答あり' : state.termination === 'not_started' ? '依頼前・実通信なし' : '確認応答なし（終了未確認）'}</dd></dl>
        <p className="caption">入力は確定時に消去し、保存・送信しません。この画面は実通信や認可の証拠ではなく、外部エージェントも起動しません。</p>
        <p className="caption">停止要求、画面を閉じること、履歴削除では終了確認になりません。終了未確認の間は新しい接続を禁止し、失効後に元の接続を復活させません。</p></Card>
      <PrototypeTools state={state} emit={emit} closeWindow={closeWindow} />
      <HistoryPanel history={history} advanceClock={advanceClock} />
    </CollapsibleContent>
  </Collapsible>;
}
