import { useEffect, useRef, useState } from 'react';
import { Laptop, Square, X } from 'lucide-react';
import { Button } from '../components/ui/button.tsx';
import { Card } from '../components/ui/card.tsx';
import { createOperatorController } from './controller.ts';
import { initialState, type Observation, type OperatorAdapter, type SampleId } from './model.ts';
import { status } from './status.ts';
import { ReceiveForm } from './ReceiveForm.tsx';
import { ConnectionPanel } from './ConnectionPanel.tsx';
import { PrototypeTools } from './PrototypeTools.tsx';
import { HistoryPanel } from './HistoryPanel.tsx';
import type { createMemoryHistory } from './history.ts';

export function OperatorApp({ adapter, emit, copy, history, advanceHistoryClock }: {
  adapter: OperatorAdapter; emit: (event: Observation) => void; copy: (text: string) => Promise<void>;
  history: ReturnType<typeof createMemoryHistory>; advanceHistoryClock: () => void;
}) {
  const [state, setState] = useState(initialState);
  const controller = useRef<ReturnType<typeof createOperatorController> | null>(null);
  const focusAfterAction = useRef<string | null>(null);
  useEffect(() => {
    const instance = createOperatorController({ adapter, history, onChange: setState });
    controller.current = instance;
    const dispose = () => { instance.dispose(); };
    window.addEventListener('pagehide', dispose, { once: true });
    return () => { instance.dispose(); controller.current = null; window.removeEventListener('pagehide', dispose); };
  }, [adapter, history]);
  useEffect(() => {
    if (focusAfterAction.current) {
      document.getElementById(focusAfterAction.current)?.focus();
      focusAfterAction.current = null;
    }
  }, [state]);
  function receive(sample: SampleId) { focusAfterAction.current = 'review-heading'; controller.current?.acceptSample(sample); }
  function reset() { if (state.termination === 'unconfirmed') return; focusAfterAction.current = 'sample-input'; controller.current?.reset(); }
  function observe(event: Observation) { focusAfterAction.current = 'operator-status-title'; emit(event); }
  const view = status[state.phase];
  const StatusIcon = view.icon;
  return <main className="operator">
    <header className="operator-header"><p className="operator-brand"><Laptop className="icon" aria-hidden="true" focusable="false" />Rescue / 操作側クライアント</p>
      <p className="operator-prototype-label">操作プロトタイプ・実接続なし</p><h1>サポート接続を準備する</h1>
      <p>外部エージェントへ渡す情報を確認します。同時に扱える接続は1件です。</p>
      {state.visibility === 'window' && <Button variant="quiet" className="operator-close" onClick={() => { focusAfterAction.current = 'tray-heading'; controller.current?.minimizeToTray(); }}><X className="icon" aria-hidden="true" focusable="false" />閉じる（トレイのモック）</Button>}</header>
    <ol className="operator-steps" aria-label="接続の流れ">{['内容を確認', '承認を待つ', '接続・作業', '終了を確認'].map((label, index) => <li key={label} aria-current={view.step === index ? 'step' : undefined}><span aria-hidden="true">{index + 1}</span>{label}</li>)}</ol>
    <section className={`status${view.role ? ` status--${view.role}` : ''}`} role="status" aria-live="polite" aria-atomic="true">
      <span className={`status__icon${state.phase === 'waiting' || state.phase === 'connecting' ? ' icon-spin' : ''}`}><StatusIcon className="icon" aria-hidden="true" focusable="false" /></span>
      <div className="status__content"><h2 id="operator-status-title" tabIndex={-1}>{view.title}</h2><p>{view.text}</p></div></section>
    <div className="operator-layout"><section aria-label="接続の操作">{state.visibility === 'tray' ? <Card><h2 id="tray-heading" tabIndex={-1}>トレイ格納中（モック）</h2>
      <p>接続状態を維持したまま、ウィンドウだけを隠す体験です。OSのトレイには格納していません。</p>
      <Button onClick={() => { focusAfterAction.current = state.sample ? 'review-heading' : 'sample-input'; controller.current?.restoreWindow(); }}>ウィンドウを戻す（モック）</Button>
      {state.validSession && !state.stopRequested && ['ready', 'connected', 'unconfirmed'].includes(state.phase) && <Button variant="danger" onClick={() => { void controller.current?.requestStop(); }}><Square className="icon" aria-hidden="true" focusable="false" />接続を停止（サンプル）</Button>}
    </Card> : state.sample ? <ConnectionPanel state={state}
      onApproval={confirmed => { void controller.current?.requestApproval(confirmed); }} onStop={() => { void controller.current?.requestStop(); }}
      onRetry={() => { void controller.current?.retryStatus(); }} copy={copy} /> : <ReceiveForm onReceive={receive} />}</section>
      <PrototypeTools state={state} emit={observe} reset={reset} /></div>
    <HistoryPanel history={history} advanceClock={advanceHistoryClock} />
    <footer className="caption">画面を閉じることや、体験を初期化することは、通信の終了確認ではありません。この入口は実通信を開始しません。</footer>
  </main>;
}
