import { useEffect, useRef, useState } from 'react';
import { Laptop, Square } from 'lucide-react';
import { Button } from '../components/ui/button.tsx';
import { Card } from '../components/ui/card.tsx';
import { createOperatorController } from './controller.ts';
import { initialState, type Observation, type OperatorAdapter, type SampleId } from './model.ts';
import { statusFor } from './status.ts';
import { ReceiveForm } from './ReceiveForm.tsx';
import { ConnectionPanel } from './ConnectionPanel.tsx';
import { OperatorDetails } from './OperatorDetails.tsx';
import type { createMemoryHistory } from './history.ts';

export function OperatorApp({ adapter, emit, copy, history, advanceHistoryClock }: {
  adapter: OperatorAdapter; emit: (event: Observation) => void; copy: (text: string) => Promise<void>;
  history: ReturnType<typeof createMemoryHistory>; advanceHistoryClock: () => void;
}) {
  const [state, setState] = useState(initialState);
  const [detailsOpen, setDetailsOpen] = useState(false);
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
  function reset() { if (state.termination === 'unconfirmed') return; focusAfterAction.current = 'sample-input'; setDetailsOpen(false); controller.current?.reset(); }
  function observe(event: Observation) { focusAfterAction.current = 'operator-status-title'; setDetailsOpen(false); emit(event); }
  function closeWindow() { focusAfterAction.current = 'tray-heading'; setDetailsOpen(false); controller.current?.minimizeToTray(); }
  const view = statusFor(state);
  const StatusIcon = view.icon;
  const description = state.phase === 'unconfirmed' && !state.validSession ? '新しい接続は始められません。接続先へ終了状況を確認してください。'
    : state.phase === 'unconfirmed' && state.checkingStatus ? '同じ接続の状態を確認しています。終了はまだ未確認です。' : view.text;
  return <main className="operator">
    <header className="operator-header"><div className="operator-topbar"><p className="operator-brand"><Laptop className="icon" aria-hidden="true" focusable="false" />Rescue / 操作側クライアント</p>
      <p className="operator-prototype-label">操作プロトタイプ・実接続なし</p></div><h1>サポート接続を準備する</h1></header>
    <section className={`status${view.role ? ` status--${view.role}` : ''}`} role="status" aria-live="polite" aria-atomic="true">
      <span className={`status__icon${state.phase === 'waiting' || state.phase === 'connecting' ? ' icon-spin' : ''}`}><StatusIcon className="icon" aria-hidden="true" focusable="false" /></span>
      <div className="status__content"><h2 id="operator-status-title" tabIndex={-1}>{view.title}</h2><p id="operator-state-description">{description}</p></div></section>
    <section aria-label="接続の操作">{state.visibility === 'tray' ? <Card><h2 id="tray-heading" tabIndex={-1}>トレイ格納中（モック）</h2>
      <p>接続状態を維持したまま、ウィンドウだけを隠す体験です。OSのトレイには格納していません。</p>
      <Button onClick={() => { focusAfterAction.current = state.sample ? 'review-heading' : 'sample-input'; controller.current?.restoreWindow(); }}>ウィンドウを戻す（モック）</Button>
      {state.validSession && !state.stopRequested && ['ready', 'connected', 'unconfirmed'].includes(state.phase) && <Button variant="danger" onClick={() => { focusAfterAction.current = 'operator-status-title'; void controller.current?.requestStop(); }}><Square className="icon" aria-hidden="true" focusable="false" />接続を停止（サンプル）</Button>}
    </Card> : state.sample ? <ConnectionPanel state={state}
      onApproval={confirmed => { void controller.current?.requestApproval(confirmed); }} onStop={() => { focusAfterAction.current = 'operator-status-title'; void controller.current?.requestStop(); }}
      onRetry={() => { void controller.current?.retryStatus(); }} onReset={reset} copy={copy} /> : <ReceiveForm onReceive={receive} />}</section>
    <OperatorDetails state={state} open={detailsOpen} onOpenChange={setDetailsOpen} emit={observe} closeWindow={closeWindow} history={history} advanceClock={advanceHistoryClock} />
  </main>;
}
