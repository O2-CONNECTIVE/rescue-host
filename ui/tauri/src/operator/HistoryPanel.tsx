import { useEffect, useRef, useState } from 'react';
import { Clock3, Trash2 } from 'lucide-react';
import { Button } from '../components/ui/button.tsx';
import { Card } from '../components/ui/card.tsx';
import type { createMemoryHistory } from './history.ts';

const results = { confirmed: '終了確認済み（モック）', unconfirmed: '終了未確認', expired: '期限切れ・終了未確認', failed: '失敗' };
export function HistoryPanel({ history, advanceClock }: {
  history: ReturnType<typeof createMemoryHistory>; advanceClock: () => void;
}) {
  const [rows, setRows] = useState(history.entries);
  const [confirmClear, setConfirmClear] = useState(false);
  const heading = useRef<HTMLHeadingElement>(null);
  const clearTrigger = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    const unsubscribe = history.subscribe(() => { setRows(history.entries()); });
    history.prune();
    return unsubscribe;
  }, [history]);
  return <Card className="operator-history" role="region" aria-labelledby="history-heading">
    <div><h2 id="history-heading" ref={heading} tabIndex={-1}>接続履歴（メモリのモック）</h2>
      <p className="caption">表示名・日時・終了結果だけを保持します。永続保存は未接続で、ページを再読込すると消えます。</p></div>
    {rows.length ? <ul className="operator-history-list">{rows.map((entry, index) => <li key={`${entry.occurredAt}-${index}`}>
      <div><h3>{entry.displayName}</h3><p className="caption"><time dateTime={new Date(entry.occurredAt).toISOString()}>{new Date(entry.occurredAt).toLocaleString('ja-JP')}</time></p><p>{results[entry.result]}</p></div>
      <Button variant="quiet" onClick={() => { history.remove(entry); heading.current?.focus(); }}><Trash2 className="icon" aria-hidden="true" focusable="false" />この履歴を削除</Button>
    </li>)}</ul> : <p>保存されたサンプル履歴はありません。</p>}
    <p id="history-help" className="caption">履歴を削除しても、接続状態や終了未確認による新規接続の禁止は変わりません。実際のPCの既存データは削除しません。</p>
    <div className="operator-history-actions"><Button ref={clearTrigger} variant="quiet" disabled={!rows.length} aria-describedby="history-help" onClick={() => { setConfirmClear(true); }}><Trash2 className="icon" aria-hidden="true" focusable="false" />サンプル履歴をすべて削除</Button>
      <Button variant="quiet" onClick={advanceClock}><Clock3 className="icon" aria-hidden="true" focusable="false" />31日経過を再現（履歴のみ）</Button></div>
    {confirmClear && <div className="operator-delete-confirm"><p>このプロトタイプのサンプル履歴をすべて削除しますか？</p>
      <Button variant="danger" onClick={() => { history.clear(); setConfirmClear(false); heading.current?.focus(); }}><Trash2 className="icon" aria-hidden="true" focusable="false" />履歴を削除する（モック）</Button>
      <Button variant="quiet" onClick={() => { setConfirmClear(false); if (clearTrigger.current?.disabled) heading.current?.focus(); else clearTrigger.current?.focus(); }}>キャンセル</Button></div>}
    <p className="caption">30日以上の履歴を削除します。履歴のモック時計だけを進め、接続の期限やOSの時計は変更しません。</p>
  </Card>;
}
