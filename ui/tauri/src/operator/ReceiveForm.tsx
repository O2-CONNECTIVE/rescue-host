import { useRef, useState } from 'react';
import { ArrowRight, FileText } from 'lucide-react';
import { Button } from '../components/ui/button.tsx';
import { Card } from '../components/ui/card.tsx';
import { readSample, samples, type SampleId } from './model.ts';

export function ReceiveForm({ onReceive }: { onReceive: (sample: SampleId) => void }) {
  const [kind, setKind] = useState<SampleId>('code');
  const [error, setError] = useState(false);
  const input = useRef<HTMLTextAreaElement>(null);
  return <Card className="operator-receive">
    <div><p className="operator-step-label">STEP 1 / 受取り</p><h2>サンプルの接続情報を入力</h2></div>
    <p id="sample-help">下のサンプルだけ使えます。実際の接続コード・鍵・接続定義を貼り付けないでください。</p>
    <form onSubmit={event => {
      event.preventDefault();
      const sample = readSample(kind, input.current?.value ?? '');
      if (input.current) input.current.value = '';
      setError(!sample);
      if (sample) onReceive(sample);
      else input.current?.focus();
    }}>
      <div className="field"><label htmlFor="sample-kind">受け取った情報の種類</label>
        <select id="sample-kind" className="input" value={kind} onChange={event => {
          setKind(event.target.value === 'definition' ? 'definition' : 'code');
          setError(false);
          if (input.current) input.current.value = '';
        }}><option value="code">接続コードのサンプル</option><option value="definition">接続定義のサンプル</option></select></div>
      <div className="field"><label htmlFor="sample-input">{kind === 'code' ? 'サンプルの接続コード' : 'サンプルの接続定義'}</label>
        <textarea ref={input} id="sample-input" className="input operator-code" rows={3} maxLength={128}
          autoComplete="off" autoCapitalize="off" spellCheck={false} placeholder={samples[kind]}
          aria-invalid={error || undefined} aria-describedby={error ? 'sample-help sample-error' : 'sample-help'} />
        {error && <p id="sample-error" className="field__error" role="alert">サンプルと一致しません。「サンプルを入力」からやり直してください。入力内容は消去しました。</p>}</div>
      <Button variant="quiet" onClick={() => {
        if (input.current) { input.current.value = samples[kind]; input.current.focus(); }
        setError(false);
      }}><FileText className="icon" aria-hidden="true" focusable="false" />サンプルを入力</Button>
      <Button type="submit" className="operator-primary"><ArrowRight className="icon" aria-hidden="true" focusable="false" />接続内容を確認</Button>
    </form>
    <p className="caption">確定時に入力欄を消去し、サンプルの種類だけを次の画面へ渡します。入力は保存・送信しません。</p>
  </Card>;
}
